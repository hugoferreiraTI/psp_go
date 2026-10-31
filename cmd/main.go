package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"psp_go/internal/db"
	"psp_go/internal/handler"
	kafkaconfig "psp_go/internal/kafka"
	"psp_go/internal/repository"
	"psp_go/internal/usecases"
)

func main() {
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("não foi possível carregar a configuração do .env")
	}
	// Configuração do usuário deste MVP, injetada no handler sem mudar SaleCard.
	keys := usecases.PaymentKeys{CMACKeyHex: os.Getenv("AES_CMAC_KEY"), GCMKeyHex: os.Getenv("AES_GCM")}
	if keys.CMACKeyHex == "" || keys.GCMKeyHex == "" {
		log.Fatal("configure AES_CMAC_KEY e AES_GCM para o usuário do MVP")
	}
	connection, err := db.ConnectDB()
	if err != nil {
		log.Fatal("não foi possível conectar ao banco de dados")
	}
	defer connection.Close()
	producer, err := kafkaconfig.NewKafkaProducer()
	if err != nil {
		log.Fatal("não foi possível configurar o produtor Kafka")
	}
	defer producer.Close()
	defer func() {
		if pending := producer.Flush(5000); pending > 0 {
			log.Printf("eventos Kafka sem confirmação ao encerrar: %d", pending)
		}
	}()
	events := kafkaconfig.NewProducerCaseDependency(producer)
	consumer, err := kafkaconfig.NewKafkaConsumer()
	if err != nil {
		log.Fatal("não foi possível configurar o consumidor Kafka")
	}
	defer consumer.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		if err := kafkaconfig.NewConsumerDependency(consumer).ConsumerGo(ctx); err != nil {
			log.Printf("consumidor Kafka interrompido: %v", err)
		}
	}()
	saleCase := usecases.NewSaleCaseDependency(repository.NewUserRepository(connection), events)
	cardHandler := handler.NewCardHandler(saleCase, keys)

	router := gin.New()
	router.Use(gin.Recovery())

	router.POST("/authorize/card", cardHandler.AuthorizeCard)
	router.POST("/card/add-value", cardHandler.AddValueInCard)
	router.POST("/card/create", cardHandler.CreatCard)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	server := &http.Server{Addr: ":" + port, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("servidor HTTP interrompido: %v", err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Print("não foi possível concluir todas as requisições ao encerrar")
	}
	stop()
	<-consumerDone
}
