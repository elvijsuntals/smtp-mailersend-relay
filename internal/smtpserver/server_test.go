package smtpserver

import "testing"

func TestSenderDomain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{in: "a@example.com", want: "example.com"},
		{in: "A@EXAMPLE.COM", want: "example.com"},
		{in: "invalid", want: ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := senderDomain(tc.in); got != tc.want {
				t.Fatalf("senderDomain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

