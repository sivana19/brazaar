package order

import "time"

type OrderItemRequest struct {
    ProductID int64 `json:"product_id" binding:"required,gt=0" example:"11"`
    Quantity  int   `json:"quantity" binding:"required,gt=0" example:"1"`
}

type CreateOrderRequest struct {
    Items []OrderItemRequest `json:"items" binding:"required,min=1"`
}

type OrderItemResponse struct {
    ID         int64 `json:"id" example:"1"`
    ProductID  int64 `json:"product_id" example:"11"`
    Quantity   int   `json:"quantity" example:"1"`
    PriceCents int64 `json:"price_cents" example:"11999"`
}

type OrderResponse struct {
    ID         int64               `json:"id" example:"1"`
    UserID     int64               `json:"user_id" example:"2"`
    Status     string              `json:"status" example:"pending"`
    TotalCents int64               `json:"total_cents" example:"11999"`
    Items      []OrderItemResponse `json:"items,omitempty"`
    CreatedAt  time.Time           `json:"created_at"`
    UpdatedAt  time.Time           `json:"updated_at"`
}

type UpdateStatusRequest struct {
    Status string `json:"status" binding:"required" example:"cancelled"`
}
