package component

import (
	"github.com/xolo-gateway/xolo/internal/core/model"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

type EnrollmentRulesPageVModel struct {
	common.AppLayoutVModel
	Org      model.Organization
	Rules    []model.EnrollmentRule
	RoleName func(value string) string
	CanWrite bool
	Success  string
}

type EnrollmentRuleFormVModel struct {
	common.AppLayoutVModel
	Org      model.Organization
	RuleID   model.EnrollmentRuleID
	Kind     model.EnrollmentKind
	Name     string
	Claim    string
	Value    string
	Role     string
	Enabled  bool
	OrgRoles []model.Role
	Error    string
}
