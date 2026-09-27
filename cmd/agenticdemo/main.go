package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/gemini"
)

func fakeLastTransaction(ctx context.Context) (*ai.LastTransaction, error) {
	fmt.Println("  [lookup fired: this demo fakes it; bot.Service wires the real transaction.Service.MostRecent instead]")
	return &ai.LastTransaction{
		Found:        true,
		Type:         "investment",
		Amount:       2000000,
		Currency:     "IDR",
		CategoryName: "mutual fund",
		Bucket:       "savings",
		Description:  "reksa dana",
		OccurredAt:   time.Now().Add(-24 * time.Hour).Format("2006-01-02"),
	}, nil
}

func run(c *gemini.Client, label, msg string) {
	fmt.Printf("=== %s ===\nMSG: %s\n", label, msg)
	results, _, steps, err := c.ParseCorrectionChained(context.Background(), msg, "IDR", "id", fakeLastTransaction)
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
	c := gemini.New(os.Getenv("GEMINI_API_KEY"), os.Getenv("GEMINI_MODEL"), "")

	run(c, "PARTIAL CORRECTION (should chain)", "eh salah, harusnya 1 juta aja deh")
	run(c, "FULL CORRECTION (should NOT chain)", "eh maaf salah, harusnya expense 50000 buat makan siang, bukan reksa dana")
	run(c, "MULTIPLE RECORDS IN ONE MESSAGE (should NOT chain, 1 round, 3 results)", "pengeluaran tadi:\nbensin mobile 300k\nmakan siang 300k\nmakan malam 100k")
}
