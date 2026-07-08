package jwtsvidutil

import (
	"testing"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
)

func TestSelectByHint(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		svids   []*jwtsvid.SVID
		hint    string
		want    *jwtsvid.SVID
		wantErr bool
	}{
		{name: "exact", svids: []*jwtsvid.SVID{nil, {Hint: "other"}, {Hint: "dsx-imds"}}, hint: "dsx-imds", want: &jwtsvid.SVID{Hint: "dsx-imds"}},
		{name: "missing", svids: []*jwtsvid.SVID{{Hint: "other"}}, hint: "dsx-imds", wantErr: true},
		{name: "duplicate", svids: []*jwtsvid.SVID{{Hint: "dsx-imds"}, {Hint: "dsx-imds"}}, hint: "dsx-imds", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := SelectByHint(test.svids, test.hint)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got.Hint != test.want.Hint {
				t.Fatalf("hint = %q, want %q", got.Hint, test.want.Hint)
			}
		})
	}
}
