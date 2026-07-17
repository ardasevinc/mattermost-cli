package retrieval

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

type searchSourceFunc func(context.Context, string, mattermost.SearchPageOptions) (mattermost.SearchPage, error)

func (f searchSourceFunc) SearchPage(ctx context.Context, teamID string, options mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
	return f(ctx, teamID, options)
}

func searchPage(posts ...mattermost.Post) mattermost.SearchPage {
	ids := make([]string, len(posts))
	for i := range posts {
		ids[i] = posts[i].ID
	}
	return mattermost.SearchPage{Posts: posts, OrderedIDs: ids, RawCount: len(ids), Matches: map[string][]string{}}
}

func TestSearchUsesLimitPlusOneDedupeAndDeterministicOrder(t *testing.T) {
	var requests []mattermost.SearchPageOptions
	result, err := Search(context.Background(), searchSourceFunc(func(_ context.Context, team string, options mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
		requests = append(requests, options)
		if team != "team" {
			t.Fatalf("team=%q", team)
		}
		if options.Page == 0 {
			page := searchPage(testPost("b", 3), testPost("a", 3), testPost("old", 2))
			page.Matches = map[string][]string{"a": {"hit"}, "old": {"drop"}}
			return page, nil
		}
		return mattermost.SearchPage{}, nil
	}), "team", "needle", SearchOptions{Limit: 2})
	if err != nil || len(requests) != 1 || requests[0].PerPage != 3 || fmt.Sprint(ids(result.Posts)) != "[a b]" || result.Completeness != CompletenessTruncated || fmt.Sprint(result.Matches["a"]) != "[hit]" {
		t.Fatalf("requests=%#v result=%#v err=%v", requests, result, err)
	}
}

func TestSearchContinuesRejectedMissingShortPagesAndEqualTimeCutoff(t *testing.T) {
	var calls int
	result, err := Search(context.Background(), searchSourceFunc(func(_ context.Context, _ string, options mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
		calls++
		switch options.Page {
		case 0:
			return mattermost.SearchPage{OrderedIDs: []string{"missing", "rejected"}, Posts: []mattermost.Post{testPost("rejected", 101)}, RawCount: 2, Incomplete: true}, nil
		case 1:
			return searchPage(testPost("d", 100), testPost("c", 100)), nil
		case 2:
			return searchPage(testPost("b", 100), testPost("a", 100)), nil
		default:
			return searchPage(testPost("older", 99)), nil
		}
	}), "team", "needle", SearchOptions{Limit: 2, Accept: func(post mattermost.Post) bool { return post.ID != "rejected" }})
	if err != nil || calls != 4 || fmt.Sprint(ids(result.Posts)) != "[a b]" || result.Completeness != CompletenessTruncated {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
}

func TestSearchExhaustionPoisonAndStagnation(t *testing.T) {
	falseValue, trueValue := false, true
	tests := []struct {
		name string
		page mattermost.SearchPage
		want Completeness
	}{
		{"empty", mattermost.SearchPage{}, CompletenessComplete},
		{"explicit false", mattermost.SearchPage{RawCount: 1, OrderedIDs: []string{"missing"}, Incomplete: true, HasNext: &falseValue}, CompletenessUnknown},
		{"empty has next", mattermost.SearchPage{HasNext: &trueValue}, CompletenessUnknown},
		{"inaccessible", mattermost.SearchPage{FirstInaccessiblePostTime: ptr64(1)}, CompletenessUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Search(context.Background(), searchSourceFunc(func(context.Context, string, mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
				return tt.page, nil
			}), "team", "x", SearchOptions{Limit: 1})
			if err != nil || result.Completeness != tt.want {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
	calls := 0
	result, err := Search(context.Background(), searchSourceFunc(func(context.Context, string, mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
		calls++
		return mattermost.SearchPage{RawCount: 1, OrderedIDs: []string{"same"}}, nil
	}), "team", "x", SearchOptions{Limit: 1})
	if err != nil || calls != 3 || result.Completeness != CompletenessUnknown {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
}

func TestSearchBoundsPagesValidatesAndPropagatesCancellation(t *testing.T) {
	calls := 0
	result, err := Search(context.Background(), searchSourceFunc(func(_ context.Context, _ string, options mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
		calls++
		return mattermost.SearchPage{RawCount: 1, OrderedIDs: []string{fmt.Sprintf("stale-%d", options.Page)}, Incomplete: true}, nil
	}), "team", "x", SearchOptions{Limit: 1})
	if err != nil || calls != MaxSearchPages || result.Completeness != CompletenessUnknown {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
	if _, err := Search(context.Background(), nil, "team", "x", SearchOptions{Limit: 1}); !errors.Is(err, ErrInvalidSearchRequest) {
		t.Fatalf("validation error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, searchSourceFunc(func(context.Context, string, mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
		panic("called")
	}), "team", "x", SearchOptions{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestSearchPropagatesLaterPageFailure(t *testing.T) {
	want := errors.New("page two failed")
	_, err := Search(context.Background(), searchSourceFunc(func(_ context.Context, _ string, options mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
		if options.Page == 0 {
			return searchPage(testPost("first", 2)), nil
		}
		return mattermost.SearchPage{}, want
	}), "team", "x", SearchOptions{Limit: 2})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestSearchNeverReconsidersFirstSeenIDs(t *testing.T) {
	calls := 0
	result, err := Search(context.Background(), searchSourceFunc(func(_ context.Context, _ string, options mattermost.SearchPageOptions) (mattermost.SearchPage, error) {
		calls++
		switch options.Page {
		case 0:
			rejected := testPost("rejected", 3)
			rejected.Message = "deny"
			return mattermost.SearchPage{OrderedIDs: []string{"missing", "deleted", "rejected"}, Posts: []mattermost.Post{rejected}, RawCount: 3, Incomplete: true}, nil
		case 1:
			missing := testPost("missing", 5)
			deleted := testPost("deleted", 4)
			rejected := testPost("rejected", 3)
			fresh := testPost("fresh", 2)
			return searchPage(missing, deleted, rejected, fresh), nil
		default:
			return mattermost.SearchPage{}, nil
		}
	}), "team", "x", SearchOptions{Limit: 4, Accept: func(post mattermost.Post) bool { return post.Message != "deny" }})
	if err != nil || calls != 3 || fmt.Sprint(ids(result.Posts)) != "[fresh]" || result.Completeness != CompletenessUnknown {
		t.Fatalf("calls=%d result=%#v error=%v", calls, result, err)
	}
}

func ptr64(value int64) *int64 { return &value }
