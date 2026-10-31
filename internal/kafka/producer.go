package kafka

import (
	"encoding/json"
	"log"
	"os"
	"strconv"

	"github.com/confluentinc/confluent-kafka-go/kafka"

	"psp_go/internal/model"
)

const Topic = "psp_authorizations"

type NewProducerCase struct {
	Producer *kafka.Producer
}

// Um único leitor de confirmações por produtor, sem goroutine por pagamento.
func NewProducerCaseDependency(producer *kafka.Producer) *NewProducerCase {
	go func() {
		for event := range producer.Events() {
			switch delivery := event.(type) {
			case *kafka.Message:
				if delivery.TopicPartition.Error != nil {
					log.Printf("falha na entrega Kafka sale_id=%s: %v", delivery.Key, delivery.TopicPartition.Error)
				}
			case kafka.Error:
				log.Printf("falha no produtor Kafka: %v", delivery)
			}
		}
	}()
	return &NewProducerCase{Producer: producer}
}

// ProducerGo enfileira o evento; a confirmação do broker é assíncrona.
// Os eventos não contêm PAN, PIN, hash, chaves nem payload EMV.
func (k *NewProducerCase) ProducerGo(event model.PaymentKafka) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	topic := Topic
	return k.Producer.Produce(&kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Key:            []byte(strconv.Itoa(event.SaleId)),
		Value:          data,
	}, nil)
}

func bootstrapServers() string {
	if value := os.Getenv("KAFKA_BOOTSTRAP_SERVERS"); value != "" {
		return value
	}
	return "localhost:9092"
}

func NewKafkaProducer() (*kafka.Producer, error) {
	return kafka.NewProducer(&kafka.ConfigMap{
		"bootstrap.servers":  bootstrapServers(),
		"acks":               "all",
		"enable.idempotence": true,
		"message.timeout.ms": 5000,
	})
}
