package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"psp_go/internal/client"
	"psp_go/internal/model"
	"psp_go/internal/repository"
	"psp_go/internal/usecases"
)

type CardHandler struct {
	saleCase    *usecases.SaleCase
	keys        usecases.PaymentKeys
	cardMachine *client.CardMachineClient
}

// NewCardHandler injeta as chaves do usuário atendido por esta instância do
// MVP. Elas vêm da configuração confiável, nunca do JSON recebido.
func NewCardHandler(saleCase *usecases.SaleCase, keys usecases.PaymentKeys, cardMachine *client.CardMachineClient) *CardHandler {
	return &CardHandler{saleCase: saleCase, keys: keys, cardMachine: cardMachine}
}

func (h *CardHandler) CreatCard(c *gin.Context) {
	var card model.Card
	if err := c.ShouldBindJSON(&card); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload", "message": "JSON de cartão inválido"})
		return
	}
	id, err := h.saleCase.CreatCard(card)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, repository.ErrCardAlreadyExists) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": "create card failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "card created", "card_id": id})
}

func (h *CardHandler) AddValueInCard(c *gin.Context) {
	var payment model.CardAddAmount
	if err := c.ShouldBindJSON(&payment); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload", "message": err.Error()})
		return
	}
	balance, err := h.saleCase.AddValueInCard(payment)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "add value failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "card balance updated", "balance": balance})
}

func (h *CardHandler) AuthorizeCard(c *gin.Context) {
	var payment model.CardPayment
	if err := c.ShouldBindJSON(&payment); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload", "message": err.Error()})
		return
	}

	status := h.saleCase.SaleCard(payment, h.keys)
	message := "payment reproved"
	if status == model.StatusApproved {
		message = "payment approved"
	}
	c.JSON(http.StatusOK, model.AuthorizationResponse{Status: status, Message: message})
	// Envia a resposta antes de iniciar o POST de retorno, sem goroutine, fila
	// ou retry. O timeout do client limita a duração da chamada de saída.
	c.Writer.Flush()
	if err := h.cardMachine.SendAuthorizationResult(c.Request.Context(), payment.SaleId, status); err != nil {
		log.Printf("falha no retorno da autorização sale_id=%d: %v", payment.SaleId, err)
	}
}
