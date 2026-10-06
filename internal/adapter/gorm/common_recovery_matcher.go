package gorm

// A sparse, byte-based Aho–Corasick automaton preserves arbitrary substring
// detection, including punctuation and overlapping IDs. Build it once per scan;
// each graph string then costs O(bytes), independent of the number of IDs.
type recoverySubstringMatcher struct {
	nodes []recoveryMatchNode
}

type recoveryMatchNode struct {
	next    map[byte]int
	failure int
	match   bool
}

func newRecoverySubstringMatcher(patterns map[string]bool) *recoverySubstringMatcher {
	m := &recoverySubstringMatcher{nodes: []recoveryMatchNode{{}}}
	for pattern := range patterns {
		state := 0
		for i := 0; i < len(pattern); i++ {
			next, ok := m.nodes[state].next[pattern[i]]
			if !ok {
				next = len(m.nodes)
				if m.nodes[state].next == nil {
					m.nodes[state].next = map[byte]int{}
				}
				m.nodes[state].next[pattern[i]] = next
				m.nodes = append(m.nodes, recoveryMatchNode{})
			}
			state = next
		}
		m.nodes[state].match = true
	}
	queue := make([]int, 0, len(m.nodes))
	for _, state := range m.nodes[0].next {
		queue = append(queue, state)
	}
	for i := 0; i < len(queue); i++ {
		state := queue[i]
		for b, next := range m.nodes[state].next {
			failure := m.advance(m.nodes[state].failure, b)
			m.nodes[next].failure = failure
			m.nodes[next].match = m.nodes[next].match || m.nodes[failure].match
			queue = append(queue, next)
		}
	}
	return m
}

func (m *recoverySubstringMatcher) advance(state int, b byte) int {
	for {
		if next, ok := m.nodes[state].next[b]; ok {
			return next
		}
		if state == 0 {
			return 0
		}
		state = m.nodes[state].failure
	}
}

func (m *recoverySubstringMatcher) contains(value string) bool {
	if m.nodes[0].match {
		return true
	}
	state := 0
	for i := 0; i < len(value); i++ {
		state = m.advance(state, value[i])
		if m.nodes[state].match {
			return true
		}
	}
	return false
}
