package main

import (
	"errors"
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"psp_go/internal/client"
	"psp_go/internal/db"
	"psp_go/internal/handler"
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

	saleCase := usecases.NewSaleCaseDependency(repository.NewUserRepository(connection))
	cardMachine := client.NewCardMachineClient(os.Getenv("CARD_MACHINE_CALLBACK_URL"), nil)
	cardHandler := handler.NewCardHandler(saleCase, keys, cardMachine)

	router := gin.New()

	router.Use(gin.Recovery())

	router.POST("/authorize/card", cardHandler.AuthorizeCard)
	router.POST("/card/add-value", cardHandler.AddValueInCard)
	router.POST("/card/create", cardHandler.CreatCard)

	if err := router.Run(); err != nil {
		log.Fatal("não foi possível iniciar o servidor HTTP da PSP")
	}
}
