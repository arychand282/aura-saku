package payment

import "testing"

// TestBuildOrderID_RoundTrip covers HandleNotification's core mechanism: the
// user id embedded in an order_id by CreateCheckout must parse back out
// exactly, since that's how a webhook callback about an order_id no
// Subscription row has been created for yet still gets attributed to the
// right user.
func TestBuildOrderID_RoundTrip(t *testing.T) {
	for _, userID := range []uint{1, 42, 999999} {
		orderID := buildOrderID(userID)
		got, ok := parseUserIDFromOrderID(orderID)
		if !ok {
			t.Fatalf("parseUserIDFromOrderID(%q) ok=false, want true", orderID)
		}
		if got != userID {
			t.Errorf("parseUserIDFromOrderID(%q) = %d, want %d", orderID, got, userID)
		}
	}
}

func TestParseUserIDFromOrderID_Invalid(t *testing.T) {
	for _, orderID := range []string{"", "not-ours-at-all", "sub-", "sub-abc-xyz"} {
		if _, ok := parseUserIDFromOrderID(orderID); ok {
			t.Errorf("parseUserIDFromOrderID(%q) ok=true, want false for an order_id this package never generated", orderID)
		}
	}
}

// TestValidSignature covers HandleNotification's first line of defense: the
// exact Midtrans-documented formula (SHA512 of order_id+status_code+
// gross_amount+ServerKey), and that a tampered field or wrong server key is
// rejected.
func TestValidSignature(t *testing.T) {
	orderID, statusCode, grossAmount, serverKey := "sub-1-abc", "200", "50000.00", "test-server-key"

	// Golden value: SHA512("sub-1-abc"+"200"+"50000.00"+"test-server-key")
	// hex-encoded — computed once via Go's crypto/sha512 and hardcoded here,
	// so this test actually pins the exact concatenation order and encoding
	// Midtrans's docs specify, rather than only checking validSignature is
	// internally self-consistent.
	const valid = "b105c68e12a0f41d8e31a2aa395f14f72067da7840ca30616555b0f4005763f9efbbae23e152f15a62a3930babcb724cbcc535791bf8ca759f64ee32c349599c"

	if !validSignature(orderID, statusCode, grossAmount, serverKey, valid) {
		t.Error("validSignature = false for a correctly computed signature, want true")
	}
	if validSignature(orderID, statusCode, grossAmount, serverKey, "0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000") {
		t.Error("validSignature = true for a garbage signature, want false")
	}
	if validSignature(orderID, statusCode, "999999.00", serverKey, valid) {
		t.Error("validSignature = true after tampering with gross_amount, want false")
	}
	if validSignature(orderID, statusCode, grossAmount, "wrong-key", valid) {
		t.Error("validSignature = true with the wrong server key, want false")
	}
}
