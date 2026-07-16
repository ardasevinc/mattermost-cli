package output

import "slices"

func GroupIntoThreads(messages []Message) []Message {
	sorted := slices.Clone(messages)
	slices.SortStableFunc(sorted, compareMessages)

	roots := make([]Message, 0, len(sorted))
	rootIndexes := make(map[string]int, len(sorted))
	orphans := make([]Message, 0)
	for _, message := range sorted {
		if canonicalRootID(message) != "" || !threadShapeKnown(message) {
			continue
		}
		root := message
		root.Replies = []Message{}
		rootIndexes[canonicalID(root)] = len(roots)
		roots = append(roots, root)
	}
	for _, message := range sorted {
		rootID := canonicalRootID(message)
		if rootID == "" && threadShapeKnown(message) {
			continue
		}
		if index, ok := rootIndexes[rootID]; ok {
			roots[index].Replies = append(roots[index].Replies, message)
		} else {
			orphans = append(orphans, message)
		}
	}
	result := append(roots, orphans...)
	slices.SortStableFunc(result, compareMessages)
	return result
}

func threadShapeKnown(message Message) bool {
	return message.CanonicalThreadShapeKnown != nil && *message.CanonicalThreadShapeKnown
}

func compareMessages(a, b Message) int {
	if order := a.Timestamp.Compare(b.Timestamp); order != 0 {
		return order
	}
	if canonicalID(a) < canonicalID(b) {
		return -1
	}
	if canonicalID(a) > canonicalID(b) {
		return 1
	}
	return 0
}

func canonicalID(message Message) string {
	if message.CanonicalID != "" {
		return message.CanonicalID
	}
	return message.ID
}

func canonicalRootID(message Message) string {
	if message.CanonicalRootID != "" {
		return message.CanonicalRootID
	}
	return message.RootID
}
