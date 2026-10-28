package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"psp_go/internal/model"
)

type CardMachineClient struct {
	url        string
	httpClient *http.Client
}

// NewCardMachineClient recebe a URL de configuração, sem endereço fixo na regra
// de negócio. Um client próprio pode ser injetado; o padrão tem timeout de 5s.
func NewCardMachineClient(url string, httpClient *http.Client) *CardMachineClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &CardMachineClient{url: url, httpClient: httpClient}
}

// SendAuthorizationResult apenas transporta a decisão. Não revalida o cartão,
// não altera status e não faz retry. O chamador trata o erro de comunicação.
func (c *CardMachineClient) SendAuthorizationResult(ctx context.Context, saleID int, status model.SaleStatus) error {
	if c.url == "" {
		return errors.New("URL de retorno da Card Machine não configurada")
	}
	payload, err := json.Marshal(model.AuthorizationResult{SaleId: saleID, Status: status})
	if err != nil {
		return fmt.Errorf("erro ao serializar resultado de autorização: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("erro ao criar requisição para Card Machine: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("erro ao enviar resultado para Card Machine: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Não inclui o corpo remoto no erro/log: ele pode conter dados sensíveis.
		return fmt.Errorf("Card Machine retornou HTTP %d", resp.StatusCode)
	}
	return nil
}
