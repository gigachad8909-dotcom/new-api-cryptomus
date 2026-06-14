package controller

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

type CryptomusPayRequest struct {
	Amount int64 `json:"amount"`
}

type CryptomusCreatePaymentRequest struct {
	Amount            string `json:"amount"`
	Currency          string `json:"currency"`
	IsPaymentMultiple bool   `json:"is_payment_multiple"`
	Lifetime          int    `json:"lifetime"`
	OrderID           string `json:"order_id"`
	URLCallback       string `json:"url_callback"`
	URLReturn         string `json:"url_return"`
}

type CryptomusPaymentResponse struct {
	State  int                    `json:"state"`
	Result CryptomusPaymentResult `json:"result"`
}

type CryptomusPaymentResult struct {
	UUID      string `json:"uuid"`
	OrderID   string `json:"order_id"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type CryptomusWebhookPayload struct {
	UUID          string `json:"uuid"`
	OrderID       string `json:"order_id"`
	Amount        string `json:"amount"`
	PaymentAmount string `json:"payment_amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	Sign          string `json:"sign"`
}

func generateCryptomusSign(data interface{}) string {
	jsonBytes, _ := json.Marshal(data)
	base64Str := base64.StdEncoding.EncodeToString(jsonBytes)
	signStr := base64Str + setting.CryptomusPaymentKey
	hash := md5.Sum([]byte(signStr))
	return hex.EncodeToString(hash[:])
}

func RequestCryptomusAmount(c *gin.Context) {
	var req CryptomusPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	if req.Amount < int64(setting.CryptomusMinTopUp) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", setting.CryptomusMinTopUp)})
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	payMoney := getCryptomusPayMoney(req.Amount, group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success", "data": fmt.Sprintf("%.2f", payMoney)})
}

func getCryptomusPayMoney(amount int64, group string) float64 {
	dAmount := decimal.NewFromInt(amount)
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dAmount = dAmount.Div(decimal.NewFromFloat(common.QuotaPerUnit))
	}

	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	discount := 1.0
	if ds, ok := operation_setting.GetPaymentSetting().AmountDiscount[int(amount)]; ok && ds > 0 {
		discount = ds
	}

	payMoney := dAmount.
		Mul(decimal.NewFromFloat(setting.CryptomusUnitPrice)).
		Mul(decimal.NewFromFloat(topupGroupRatio)).
		Mul(decimal.NewFromFloat(discount))

	return payMoney.InexactFloat64()
}

func RequestCryptomus(c *gin.Context) {
	var req CryptomusPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	if req.Amount < int64(setting.CryptomusMinTopUp) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", setting.CryptomusMinTopUp)})
		return
	}

	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	payMoney := getCryptomusPayMoney(req.Amount, group)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	// Generate unique trade number
	tradeNo := fmt.Sprintf("USR%dNO%s", id, randstr.String(16))

	// Prepare Cryptomus payment request
	paymentData := CryptomusCreatePaymentRequest{
		Amount:            fmt.Sprintf("%.2f", payMoney),
		Currency:          "USD",
		IsPaymentMultiple: false,
		Lifetime:          3600,
		OrderID:           tradeNo,
		URLCallback:       setting.CryptomusCallbackURL,
		URLReturn:         setting.CryptomusReturnURL,
	}

	sign := generateCryptomusSign(paymentData)

	// Call Cryptomus API
	client := &http.Client{Timeout: 10 * time.Second}
	jsonData, _ := json.Marshal(paymentData)

	httpReq, err := http.NewRequest("POST", "https://api.cryptomus.com/v1/payment", bytes.NewBuffer(jsonData))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("merchant", setting.CryptomusMerchantID)
	httpReq.Header.Set("sign", sign)

	resp, err := client.Do(httpReq)
	if err != nil {
		logger.LogError(c.Request.Context(),"Cryptomus API request failed: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var paymentResp CryptomusPaymentResponse
	if err := json.Unmarshal(body, &paymentResp); err != nil {
		logger.LogError(c.Request.Context(), "Cryptomus response parse failed: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "解析支付响应失败"})
		return
	}

	if paymentResp.State != 0 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}

	// Store order in database
	normalizedAmount := normalizeCryptomusTopUpAmount(req.Amount)
	topUp := &model.TopUp{
		UserId:          id,
		Amount:          normalizedAmount,
		Money:           payMoney,
		TradeNo:         tradeNo,
		PaymentMethod:   model.PaymentMethodCryptomus,
		PaymentProvider: model.PaymentProviderCryptomus,
		CreateTime:      time.Now().Unix(),
		Status:          "pending",
	}

	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(),"Failed to insert topup order: " + err.Error())
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data":    paymentResp.Result.URL,
	})
}

func normalizeCryptomusTopUpAmount(amount int64) int64 {
	if operation_setting.GetQuotaDisplayType() != operation_setting.QuotaDisplayTypeTokens {
		return amount
	}

	normalized := decimal.NewFromInt(amount).
		Div(decimal.NewFromFloat(common.QuotaPerUnit)).
		IntPart()
	if normalized < 1 {
		return 1
	}
	return normalized
}

func CryptomusNotify(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logger.LogError(c.Request.Context(),"Failed to read Cryptomus webhook body: " + err.Error())
		c.String(http.StatusBadRequest, "fail")
		return
	}

	var payload CryptomusWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.LogError(c.Request.Context(),"Failed to parse Cryptomus webhook: " + err.Error())
		c.String(http.StatusBadRequest, "fail")
		return
	}

	// Verify signature
	receivedSign := payload.Sign
	payloadCopy := payload
	payloadCopy.Sign = ""

	expectedSign := generateCryptomusSign(payloadCopy)
	if receivedSign != expectedSign {
		logger.LogError(c.Request.Context(),"Invalid Cryptomus webhook signature")
		c.String(http.StatusForbidden, "fail")
		return
	}

	// Return success immediately
	c.String(http.StatusOK, "success")

	// Process payment asynchronously
	if payload.Status != "paid" && payload.Status != "paid_over" {
		logger.LogInfo(c.Request.Context(),fmt.Sprintf("Cryptomus payment %s status: %s - no action", payload.OrderID, payload.Status))
		return
	}

	// Lock order to prevent concurrent processing
	LockOrder(payload.OrderID)
	defer UnlockOrder(payload.OrderID)

	// Find order
	topUp := model.GetTopUpByTradeNo(payload.OrderID)
	if topUp == nil {
		logger.LogError(c.Request.Context(),"Cryptomus order not found: " + payload.OrderID)
		return
	}

	// Check if already processed
	if topUp.Status != "pending" {
		logger.LogInfo(c.Request.Context(),fmt.Sprintf("Cryptomus order %s already processed with status: %s", payload.OrderID, topUp.Status))
		return
	}

	// Update order status
	topUp.Status = "success"
	topUp.CompleteTime = time.Now().Unix()
	if err := topUp.Update(); err != nil {
		logger.LogError(c.Request.Context(),"Failed to update Cryptomus order: " + err.Error())
		return
	}

	// Credit user account
	err = model.IncreaseUserQuota(topUp.UserId, int(topUp.Amount), true)
	if err != nil {
		logger.LogError(c.Request.Context(),fmt.Sprintf("Failed to credit user %d quota: %s", topUp.UserId, err.Error()))
		return
	}

	// Log transaction
	model.RecordLog(topUp.UserId, model.LogTypeTopup, fmt.Sprintf("充值成功，金额：%s，订单号：%s",
		logger.LogQuota(int(topUp.Amount)), payload.OrderID))

	logger.LogInfo(c.Request.Context(),fmt.Sprintf("Cryptomus payment processed: user=%d, amount=%d, order=%s",
		topUp.UserId, topUp.Amount, payload.OrderID))
}

func isCryptomusTopUpEnabled() bool {
	return setting.CryptomusMerchantID != "" && setting.CryptomusPaymentKey != ""
}
