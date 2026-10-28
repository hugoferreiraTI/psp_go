package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"psp_go/internal/model"
	"psp_go/internal/repository"
	"psp_go/internal/usecases"
)

type CardHandler struct {
	saleCase *usecases.SaleCase
	keys     usecases.PaymentKeys
}

// NewCardHandler injeta as chaves do usuário atendido por esta instância do
// MVP. Elas vêm da configuração confiável, nunca do JSON recebido.
func NewCardHandler(saleCase *usecases.SaleCase, keys usecases.PaymentKeys) *CardHandler {
	return &CardHandler{saleCase: saleCase, keys: keys}
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
	fmt.Println("[AuthorizeCard] POST /authorize/card recebido")
	var payment model.CardPayment
	if err := c.ShouldBindJSON(&payment); err != nil {
		fmt.Println("[AuthorizeCard] bind do JSON falhou; resposta HTTP 400")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload", "message": err.Error()})
		return
	}

	fmt.Printf("[AuthorizeCard] sale_id=%d JSON decodificado; chamando SaleCard\n", payment.SaleId)
	result := h.saleCase.SaleCard(payment, h.keys)
	c.JSON(http.StatusOK, result)
	fmt.Printf("[AuthorizeCard] sale_id=%d resposta HTTP 200 status=%s\n", payment.SaleId, result.Status)
}
