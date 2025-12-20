package services

// isLocalGeneratedOrderID 检查是否是本地生成的订单ID
// 本地生成的订单ID通常以 "entry-", "hedge-", "smart-" 开头
func isLocalGeneratedOrderID(orderID string) bool {
	if orderID == "" {
		return false
	}
	// 检查是否是本地生成的ID格式
	if len(orderID) > 10 && orderID[:10] == "entry-up-" {
		return true
	}
	if len(orderID) > 12 && orderID[:12] == "hedge-down-" {
		return true
	}
	if len(orderID) > 5 && orderID[:5] == "smart" {
		return true
	}
	if len(orderID) > 6 && orderID[:6] == "entry-" {
		return true
	}
	if len(orderID) > 6 && orderID[:6] == "hedge-" {
		return true
	}
	return false
}
