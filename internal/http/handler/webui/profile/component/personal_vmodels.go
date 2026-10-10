package component

import (
	"github.com/xolo-gateway/xolo/internal/core/model"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

type PersonalModelsPageVModel struct {
	common.AppLayoutVModel
	VirtualModels []model.PersonalVirtualModel
	// Selected is the model whose pipeline the right pane previews. It is the
	// first of the list unless `?vm=` names another, exactly as on the
	// organisation screen.
	Selected model.PersonalVirtualModel
	BaseURL  string
	Success       string
	Error         string
}

type PersonalModelFormVModel struct {
	common.AppLayoutVModel
	VirtualModel model.PersonalVirtualModel
	IsNew        bool
	Name         string
	Description  string
	Error        string
	// Teams are the user's teams; TeamID the one the model charges.
	Teams  []TeamOption
	TeamID model.TeamID
}

type PersonalModelEditorVModel struct {
	common.AppLayoutVModel
	VM      model.PersonalVirtualModel
	APIBase string
}

// TeamOption is a team the user belongs to, offered when binding a token or a
// personal virtual model to a team.
type TeamOption struct {
	OrgID    model.OrgID
	OrgName  string
	TeamID   model.TeamID
	TeamName string
	Slug     string
}

// Label names the option with its organization.
func (o TeamOption) Label() string {
	return o.OrgName + " · " + o.TeamName
}
