package stricturl

import "testing"

func TestParseHTTP(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value   string
		wantErr bool
	}{
		{value: "https://exchange.example/keys?set=active"},
		{value: "file:///tmp/keys", wantErr: true},
		{value: "https:///keys", wantErr: true},
		{value: " https://exchange.example/keys", wantErr: true},
	} {
		if _, err := ParseHTTP(test.value); (err != nil) != test.wantErr {
			t.Errorf("ParseHTTP(%q) = %v, wantErr %v", test.value, err, test.wantErr)
		}
	}
}

func TestParseHTTPOrigin(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value   string
		wantErr bool
	}{
		{value: "http://spiffe-exchange.dsx-identity.svc.cluster.local:8080"},
		{value: "http://spiffe-exchange/", wantErr: true},
		{value: "http://spiffe-exchange?set=active", wantErr: true},
		{value: "http://user@spiffe-exchange", wantErr: true},
	} {
		if _, err := ParseHTTPOrigin(test.value); (err != nil) != test.wantErr {
			t.Errorf("ParseHTTPOrigin(%q) = %v, wantErr %v", test.value, err, test.wantErr)
		}
	}
}
