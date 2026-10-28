package product_test

import (
	"errors"
	"testing"

	"bazaar/internal/product"
)

func TestCreateProductInput_Validate(t *testing.T) {
	tests := []struct {
		name        string
		input       product.CreateProductInput
		wantErr     bool
		expectedErr error
	}{
		{
			name: "valid product input",
			input: product.CreateProductInput{
				Name:        "Wireless Mechanical Keyboard",
				Description: "Compact 75% hot-swappable keyboard",
				PriceCents:  8999,
				Stock:       10,
			},
			wantErr:     false,
			expectedErr: nil,
		},
		{
			name: "empty name fails",
			input: product.CreateProductInput{
				Name:        "   ",
				Description: "No name provided",
				PriceCents:  4999,
				Stock:       5,
			},
			wantErr:     true,
			expectedErr: product.ErrEmptyName,
		},
		{
			name: "zero price fails",
			input: product.CreateProductInput{
				Name:        "Free Sample",
				Description: "Zero price item",
				PriceCents:  0,
				Stock:       10,
			},
			wantErr:     true,
			expectedErr: product.ErrInvalidPrice,
		},
		{
			name: "negative price fails",
			input: product.CreateProductInput{
				Name:        "Negative Price Item",
				Description: "Negative price error",
				PriceCents:  -1500,
				Stock:       2,
			},
			wantErr:     true,
			expectedErr: product.ErrInvalidPrice,
		},
		{
			name: "negative stock fails",
			input: product.CreateProductInput{
				Name:        "Negative Stock Item",
				Description: "Stock cannot be negative",
				PriceCents:  2500,
				Stock:       -1,
			},
			wantErr:     true,
			expectedErr: product.ErrNegativeStock,
		},
		{
			name: "zero stock is valid",
			input: product.CreateProductInput{
				Name:        "Backordered Mouse",
				Description: "Stock is zero but valid",
				PriceCents:  3500,
				Stock:       0,
			},
			wantErr:     false,
			expectedErr: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.input.Validate()

			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Errorf("expected error %v, got %v", tc.expectedErr, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
