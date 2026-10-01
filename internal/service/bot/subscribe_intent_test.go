package bot

import "testing"

func TestIsSubscribeIntentText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"indonesian desire word plus pro", "mau pro dong", true},
		{"bare subscribe", "I want to subscribe please", true},
		{"bare berlangganan", "cara berlangganan gimana?", true},
		{"upgrade with dong", "upgrade dong", true},
		{"upgrade ke pro", "gimana cara upgrade ke pro?", true},
		{"english want pro", "I want pro", true},
		{"product name iPhone Pro not hijacked", "beli iPhone 15 Pro 20jt", false},
		{"product name MacBook Pro not hijacked", "beli macbook pro 25jt buat kerja", false},
		{"unrelated upgrade purchase not hijacked", "upgrade RAM laptop 500rb", false},
		{"ordinary transaction text", "makan siang di warteg 15000", false},
		{"empty text", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSubscribeIntentText(tc.text)
			if got != tc.want {
				t.Errorf("isSubscribeIntentText(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
