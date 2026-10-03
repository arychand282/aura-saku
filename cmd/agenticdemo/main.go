package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/gemini"
)

func fakeRecentTransactions(ctx context.Context) ([]*ai.RecentTransaction, error) {
	fmt.Println("  [lookup fired: this demo fakes it; bot.Service wires the real transaction.Service.RecentTransactions instead]")
	return []*ai.RecentTransaction{
		{
			ID:           3,
			Type:         "expense",
			Amount:       100000,
			Currency:     "IDR",
			CategoryName: "food",
			Bucket:       "needs",
			Description:  "makan malam",
			OccurredAt:   time.Now().Format("2006-01-02"),
		},
		{
			ID:           2,
			Type:         "expense",
			Amount:       300000,
			Currency:     "IDR",
			CategoryName: "food",
			Bucket:       "needs",
			Description:  "makan siang",
			OccurredAt:   time.Now().Format("2006-01-02"),
		},
		{
			ID:           1,
			Type:         "investment",
			Amount:       2000000,
			Currency:     "IDR",
			CategoryName: "mutual fund",
			Bucket:       "savings",
			Description:  "reksa dana",
			OccurredAt:   time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
		},
	}, nil
}

func fakeRecurringTransactions(ctx context.Context) ([]*ai.RecurringCandidate, error) {
	fmt.Println("  [lookup fired: this demo fakes it; bot.Service wires the real recurring.Service.ListActive instead]")
	return []*ai.RecurringCandidate{
		{ID: 1, Type: "expense", Amount: 150000, Currency: "IDR", CategoryName: "entertainment", Description: "netflix", DayOfMonth: 5, ReminderOnly: false},
		{ID: 2, Type: "expense", Amount: 800000, Currency: "IDR", CategoryName: "rent", Description: "sewa kos", DayOfMonth: 1, ReminderOnly: true},
	}, nil
}

func run(c *gemini.Client, label, msg string) {
	fmt.Printf("=== %s ===\nMSG: %s\n", label, msg)
	results, _, steps, err := c.ParseCorrectionChained(context.Background(), msg, "IDR", "id", fakeRecentTransactions, fakeRecurringTransactions, ai.ConversationContext{})
	for i, s := range steps {
		fmt.Printf("  round %d: called %s(%v)\n", i+1, s.ToolCalled, s.Args)
	}
	fmt.Printf("  RESULT (%d):\n", len(results))
	for i, pt := range results {
		fmt.Printf("    [%d] %+v\n", i+1, *pt)
	}
	fmt.Printf("  ERR: %v\n\n", err)
}

func main() {
	c := gemini.New(os.Getenv("GEMINI_API_KEY"), os.Getenv("GEMINI_MODEL"), "", "")

	run(c, "PARTIAL CORRECTION (should chain)", "eh salah, harusnya 1 juta aja deh")
	run(c, "FULL CORRECTION (should NOT chain)", "eh maaf salah, harusnya expense 50000 buat makan siang, bukan reksa dana")
	run(c, "MULTIPLE RECORDS IN ONE MESSAGE (should NOT chain, 1 round, 3 results)", "pengeluaran tadi:\nbensin mobile 300k\nmakan siang 300k\nmakan malam 100k")
	run(c, "CORRECTION TARGETING A NON-LATEST TRANSACTION (should chain, target_transaction_id=2)", "eh yang makan siang tadi salah, harusnya 350rb bukan 300rb")
	run(c, "DELETE A NON-LATEST TRANSACTION (should chain, delete_transaction target_transaction_id=2)", "hapus transaksi makan siang tadi")
	run(c, "DELETE THE OBVIOUSLY-LATEST TRANSACTION (may or may not chain, delete_transaction target_transaction_id=3 or omitted)", "eh batalin transaksi makan malam tadi")
	run(c, "CANCEL A RECURRING TRANSACTION BY NAME (should chain, delete_recurring_transaction target_recurring_transaction_id=1)", "batalin netflix")
	run(c, "CANCEL A BILL REMINDER BY CATEGORY (should chain, delete_recurring_transaction target_recurring_transaction_id=2)", "stop pengingat sewa kos")
}
