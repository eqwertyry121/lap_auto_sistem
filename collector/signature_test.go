package collector

import "testing"

// Регрессия: подпись должна совпадать с клиентской generateSignature KP.
// Эталоны получены из реального JS-бандла сайта (проверено на живом API).
func TestKpSalt(t *testing.T) {
	const want = "NNNNNvvvvvttttt33333ZZZZZRRRRRKKKKK4nGz7f6Gd|pE|5"
	if kpSalt != want {
		t.Fatalf("kpSalt mismatch:\n got %q\nwant %q", kpSalt, want)
	}
}

func TestKpSignatureSearch(t *testing.T) {
	path := "/api/web/v1/search?order=posted+desc&page=1&firstParam=kompjuteri-laptop-i-tablet&group=laptopovi&categoryId=1221&groupId=101&attributeSummaryType=summaryShort"
	const want = "985fccbde9ba4cb92a46268b03055f0f23de19c3" // эталон из Node/JS
	if got := kpSignature(path, nil); got != want {
		t.Fatalf("signature mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestKpSignatureEds(t *testing.T) {
	path := "/api/web/v1/eds/151982322"
	// SHA1(path+salt), эталон посчитан независимо в Node/JS.
	const want = "5d59df4e5150ea2b9592b8d1ff039678d910e10a"
	if got := kpSignature(path, nil); got != want {
		t.Fatalf("signature mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestIsChallengeBody(t *testing.T) {
	body := []byte(`{"success":true,"info":{"captcha":"areYouHuman","captchaSiteKey":"site-key"}}`)
	if !isChallengeBody(body) {
		t.Fatal("captcha response must be recognized as challenge")
	}
	if isChallengeBody([]byte(`{"success":true,"info":{"name":"Lenovo ThinkPad"}}`)) {
		t.Fatal("normal detail response must not be challenge")
	}
}
