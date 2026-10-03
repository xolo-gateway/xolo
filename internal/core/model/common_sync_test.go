package model

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCommonMatchCondition(t *testing.T) {
	for _, raw := range []string{`*`, `W/"u-123"`, `"u-123"`, `"other", W/"u-123"`, `W/"a,b", "u-123"`} {
		c, err := ParseMatchCondition([]string{raw})
		require.NoError(t, err, raw)
		require.True(t, c.Matches(`W/"u-123"`))
		require.False(t, c.Matches(""))
	}
	for _, raw := range []string{"", `*, "x"`, `w/"x"`, `"x",`, `x`, `"x" "y"`, `"a b"`, `W/`, "\"a\x7fb\""} {
		_, err := ParseMatchCondition([]string{raw})
		require.Error(t, err, raw)
	}
	c, err := ParseMatchCondition(nil)
	require.NoError(t, err)
	require.True(t, c.Matches(""))
}
