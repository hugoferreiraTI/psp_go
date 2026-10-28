package usecases

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hugoferreiraTI/AES_CMAC_Go/cmac"
	"golang.org/x/crypto/bcrypt"

	"psp_go/internal/model"
)

var (
	ErrInvalidCryptogram = errors.New("cryptograma inválido")
	ErrInvalidAESGCM     = errors.New("payload AES-GCM inválido ou não autenticado")
	ErrInvalidPaymentKey = errors.New("chave criptográfica inválida")
	ErrInvalidPIN        = errors.New("PIN incorreto")
	ErrInvalidPINHash    = errors.New("hash do PIN ausente ou inválido")
)

// PaymentKeys contém as chaves compartilhadas com a máquina daquele usuário.
// O chamador fornece as chaves do contexto autenticado, fora do CardPayment/JSON.
// Não há chave global, valor padrão ou leitura de variáveis de ambiente aqui.
type PaymentKeys struct {
	CMACKeyHex string
	GCMKeyHex  string
}

// VerifyPIN compara o PIN recuperado em memória com o bcrypt cadastrado.
// Não gera outro hash, não normaliza o PIN e não inclui PIN/hash no erro.
func VerifyPIN(pin, pinHash string) error {
	// Mantém o mesmo limite de entrada de bcrypt.GenerateFromPassword.
	if len(pin) > 72 {
		return ErrInvalidPIN
	}
	err := bcrypt.CompareHashAndPassword([]byte(pinHash), []byte(pin))
	if err == nil {
		return nil
	}
	if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return ErrInvalidPIN
	}
	return ErrInvalidPINHash
}

// VerifyCryptogram reconstrói a mensagem exatamente como o card_machine.
// Amount é a string original ("25.33"), não o inteiro em centavos. O número
// imprevisível participa como 4 bytes, sem conversão para texto hexadecimal.
// O protocolo deste MVP usa o CMAC completo de 16 bytes na tag 9F26.
func VerifyCryptogram(payment model.CardPayment, keyHex string) error {
	cryptogram := payment.Emv.Cryptogram
	if !bytes.Equal(cryptogram.Tag, []byte{0x9F, 0x26}) ||
		cryptogram.Length != len(cryptogram.Value) || len(cryptogram.Value) != aes.BlockSize {
		return ErrInvalidCryptogram
	}
	unpredictable := payment.Emv.NumberUnpredictable
	if !bytes.Equal(unpredictable.Tag, []byte{0x9F, 0x37}) ||
		unpredictable.Length != len(unpredictable.Value) || len(unpredictable.Value) != 4 {
		return ErrInvalidCryptogram
	}

	var number [4]byte
	copy(number[:], unpredictable.Value)
	data := model.Cryptogram{
		StatusCard:               payment.Status,
		AmountCrypt:              payment.Amount,
		TimeNowCrypt:             payment.TimeNow,
		NumberUnpredictableCrypt: number,
	}
	message := fmt.Sprintf("%s|%s|%s|%s",
		data.StatusCard, data.AmountCrypt, data.TimeNowCrypt, data.NumberUnpredictableCrypt)

	// Verify recalcula o CMAC e compara os 16 bytes em tempo constante.
	valid, err := cmac.Verify(keyHex, message, hex.EncodeToString(cryptogram.Value))
	if err != nil {
		return fmt.Errorf("%w: configuração CMAC", ErrInvalidPaymentKey)
	}
	if !valid {
		return ErrInvalidCryptogram
	}
	return nil
}

// VerifyAESGCM autentica e descriptografa o PIN recebido em hexadecimal no
// formato nonce || ciphertext || tag, gerado com Seal e AAD nil na máquina.
// Só devolve o PIN quando Open valida a autenticação; não compara a senha
// com um cadastro bancário. O PIN retornado não deve ser registrado em logs.
func VerifyAESGCM(payloadHex, keyHex string) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("%w: configuração AES-GCM", ErrInvalidPaymentKey)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("%w: configuração AES-GCM", ErrInvalidPaymentKey)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	payload, err := hex.DecodeString(payloadHex)
	if err != nil || len(payload) < gcm.NonceSize()+gcm.Overhead() {
		return "", ErrInvalidAESGCM
	}
	nonce := payload[:gcm.NonceSize()]
	ciphertext := payload[gcm.NonceSize():]
	pin, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", ErrInvalidAESGCM
	}
	return string(pin), nil
}
