package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommonMatchCondition(t *testing.T) {
	for _, raw := range []string{`*`, `W/"123"`, `"123"`, `"other", W/"123"`, `W/"a,b", "123"`} {
		c, err := ParseMatchCondition([]string{raw})
		require.NoError(t, err, raw)
		require.True(t, c.Matches(`W/"123"`))
		require.False(t, c.Matches(""))
	}
	for _, raw := range []string{"", `*, "x"`, `w/"x"`, `"x",`, `x`, `"x" "y"`, `"a b"`, `W/`, "\"a\x7fb\""} {
		_, err := ParseMatchCondition([]string{raw})
		require.Error(t, err, raw)
	}
	c, err := ParseMatchCondition(nil)
	require.NoError(t, err)
	require.True(t, c.Matches(""))

	stale, err := ParseMatchCondition([]string{`W/"12"`})
	require.NoError(t, err)
	require.False(t, stale.Matches(CommonETag(123)))
}

func TestCommonETagIsTheRevision(t *testing.T) {
	require.Equal(t, `W/"42"`, CommonETag(42))
	require.NotEqual(t, CommonETag(42), CommonETag(43))
	require.Equal(t, "organization_membership.deleted.v1", CommonEventType(FamilyOrganizationMembership, CommonEventDeleted))
}
