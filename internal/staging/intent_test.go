package staging

import (
	"encoding/hex"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
)

func TestCallerIntentDigestGoldenAndEscapeAwareness(t *testing.T) {
	target := conversationIntent{"direct", "username", "hakan", nil}
	for _, test := range []struct{ body, want string }{
		{"x\u2028", "fd8d62e0c64b8321387d4abc316bef460ed1a84e8f909e5f0266032b06b89c9b"},
		{`x\u2028`, "23158033b33d3823fa7638c55b00df9383f0747539d231382f40154397dc9b19"},
	} {
		digest := intentDigest(stagestore.CreatePost, target, []byte(test.body), "", []stageinput.MetadataIntent{})
		if got := hex.EncodeToString(digest[:]); got != test.want {
			t.Fatalf("body %q digest = %s", test.body, got)
		}
	}
}

func TestCallerIntentBindsOrderedNormalizedAttachmentMetadata(t *testing.T) {
	a := "text/plain"
	first := []stageinput.MetadataIntent{{Path: "/a", RemoteFilename: "a", MediaType: &a}, {Path: "/b", RemoteFilename: "b"}}
	second := []stageinput.MetadataIntent{first[1], first[0]}
	if intentDigest(stagestore.Reply, postIntent{"post"}, []byte("body"), "", first) == intentDigest(stagestore.Reply, postIntent{"post"}, []byte("body"), "", second) {
		t.Fatal("attachment order was not bound")
	}
}
