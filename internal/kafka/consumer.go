package kafka

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/confluentinc/confluent-kafka-go/kafka"

	"psp_go/internal/model"
)

type NewConsumerCase struct {
	Consumer *kafka.Consumer
}

func NewConsumerDependency(consumer *kafka.Consumer) *NewConsumerCase {
	return &NewConsumerCase{Consumer: consumer}
}

// ConsumerGo observa os eventos da PSP; não executa nem repete autorizações.
func (c *NewConsumerCase) ConsumerGo(ctx context.Context) error {
	if err := c.Consumer.SubscribeTopics([]string{Topic}, nil); err != nil {
		return err
	}
	for ctx.Err() == nil {
		message, err := c.Consumer.ReadMessage(100 * time.Millisecond)
		if err != nil {
			if failure, ok := err.(kafka.Error); ok && failure.Code() == kafka.ErrTimedOut {
				continue
			}
			log.Printf("falha no consumidor Kafka: %v", err)
			continue
		}
		var event model.PaymentKafka
		if err := json.Unmarshal(message.Value, &event); err != nil {
			log.Print("evento Kafka inválido; conteúdo omitido")
			continue
		}
		log.Printf("evento PSP sale_id=%d amount=%s payment_type=%s status=%s", event.SaleId, event.Value, event.PaymentType, event.Status)
	}
	return nil
}

func NewKafkaConsumer() (*kafka.Consumer, error) {
	return kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers": bootstrapServers(),
		"group.id":          "psp-authorizations",
		"auto.offset.reset": "earliest",
	})
}
