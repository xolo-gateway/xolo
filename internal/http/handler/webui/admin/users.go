package admin

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"strconv"

	"github.com/a-h/templ"
	"github.com/bornholm/go-x/templx/form"
	formx "github.com/bornholm/go-x/templx/form"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/admin/component"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/common"
	commonComp "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/http/middleware/authz"
	"github.com/xolo-gateway/xolo/templx/form/renderer/templui"
	"github.com/pkg/errors"
)

func (h *Handler) getUsersPage(w http.ResponseWriter, r *http.Request) {
	vmodel, err := h.fillUsersPageViewModel(r)
	if err != nil {
		common.HandleError(w, r, errors.WithStack(err))
		return
	}

	usersPage := component.UsersPage(*vmodel)
	templ.Handler(usersPage).ServeHTTP(w, r)
}

func (h *Handler) getEditUserPage(w http.ResponseWriter, r *http.Request) {
	vmodel, err := h.fillEditUserPageViewModel(r)
	if err != nil {
		common.HandleError(w, r, errors.WithStack(err))
		return
	}

	editUserPage := component.EditUserPage(*vmodel)
	templ.Handler(editUserPage).ServeHTTP(w, r)
}

func (h *Handler) postEditUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	currentUser := httpCtx.User(ctx)
	if currentUser == nil {
		common.HandleError(w, r, errors.New("user not found in context"))
		return
	}

	userID := model.UserID(r.PathValue("id"))
	if userID == "" {
		common.HandleError(w, r, errors.New("user ID is required"))
		return
	}

	// Get existing user
	existingUser, err := h.userStore.GetUserByID(ctx, userID)
	if err != nil {
		common.HandleError(w, r, errors.WithStack(err))
		return
	}

	form := h.newUserForm()
	if err := form.Handle(r); err != nil {
		common.HandleError(w, r, errors.Wrap(err, "could not handle form"))
		return
	}

	if !form.IsValid(ctx) {
		vmodel, err := h.fillEditUserPageViewModel(r)
		if err != nil {
			common.HandleError(w, r, errors.WithStack(err))
			return
		}

		vmodel.UserForm = form

		editUserPage := component.EditUserPage(*vmodel)
		templ.Handler(editUserPage).ServeHTTP(w, r)

		return
	}

	// Get form values
	rolesStr, _ := form.GetFieldValues("roles")
	activeStr, _ := form.GetFieldValue("active")

	// Convert active status
	active := activeStr == "true"

	// Create updated user with only editable fields changed
	updatedUser := model.CopyUser(existingUser)
	updatedUser.SetRoles(rolesStr...)
	updatedUser.SetActive(active)

	err = h.userStore.SaveUser(ctx, updatedUser)
	if err != nil {
		common.HandleError(w, r, errors.WithStack(err))
		return
	}

	// Redirect to users list
	redirectURL := commonComp.BaseURL(r.Context(), commonComp.WithPath("/admin/users"))
	http.Redirect(w, r, string(redirectURL), http.StatusSeeOther)
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := model.UserID(r.PathValue("id"))
	if userID == "" {
		common.HandleError(w, r, errors.New("user ID is required"))
		return
	}

	err := h.userStore.DeleteUser(ctx, userID)
	if err != nil {
		common.HandleError(w, r, errors.WithStack(err))
		return
	}

	// Redirect to users list
	redirectURL := commonComp.BaseURL(r.Context(), commonComp.WithPath("/admin/users"))
	http.Redirect(w, r, string(redirectURL), http.StatusSeeOther)
}

func (h *Handler) fillUsersPageViewModel(r *http.Request) (*component.UsersPageVModel, error) {
	vmodel := &component.UsersPageVModel{}
	ctx := r.Context()

	err := common.FillViewModel(
		ctx,
		vmodel, r,
		h.fillUsersPageVModelAppLayout,
		h.fillUsersPageVModelUsers,
	)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return vmodel, nil
}

func (h *Handler) fillEditUserPageViewModel(r *http.Request) (*component.EditUserPageVModel, error) {
	vmodel := &component.EditUserPageVModel{}
	ctx := r.Context()

	userID := model.UserID(r.PathValue("id"))
	if userID == "" {
		return nil, errors.New("user ID is required")
	}

	err := common.FillViewModel(
		ctx,
		vmodel, r,
		h.fillEditUserPageVModelAppLayout,
		h.fillEditUserPageVModelUser,
		h.fillEditUserPageVModelForm,
	)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return vmodel, nil
}

func (h *Handler) fillUsersPageVModelAppLayout(ctx context.Context, vmodel *component.UsersPageVModel, r *http.Request) error {
	user := httpCtx.User(ctx)
	if user == nil {
		return errors.New("could not retrieve user from context")
	}

	isAdmin := slices.Contains(user.Roles(), authz.RoleAdmin)

	vmodel.AppLayoutVModel = commonComp.AppLayoutVModel{
		User:         user,
		IsAdmin:      isAdmin,
		SelectedItem: "users",
		Breadcrumbs: []commonComp.BreadcrumbItem{
			{Label: "Plateforme", Href: "/admin/"},
			{Label: "Utilisateurs", Href: "/admin/users"},
		},
		Context: commonComp.ContextPlatform,
	}

	return nil
}

func (h *Handler) fillUsersPageVModelUsers(ctx context.Context, vmodel *component.UsersPageVModel, r *http.Request) error {
	// Parse pagination parameters
	page := 0
	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p - 1 // Convert to 0-based
		}
	}

	limit := 10
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	search := strings.TrimSpace(r.URL.Query().Get("q"))

	// The platform console is tenant-scoped: a platform admin administers the
	// accounts of its own tenant, never those of the whole instance.
	tenantID := httpCtx.TenantID(ctx)

	filterOpts := port.QueryUsersOptions{Search: search, TenantID: &tenantID}
	opts := port.QueryUsersOptions{
		Page:     &page,
		Limit:    &limit,
		Search:   search,
		TenantID: &tenantID,
	}

	total, err := h.userStore.CountUsers(ctx, filterOpts)
	if err != nil {
		return errors.WithStack(err)
	}

	users, err := h.userStore.QueryUsers(ctx, opts)
	if err != nil {
		return errors.WithStack(err)
	}

	// The subtitle states how many accounts are deactivated. It is a count on the
	// unfiltered set, so it keeps meaning something while a search is active.
	inactive := false
	inactiveCount, err := h.userStore.CountUsers(ctx, port.QueryUsersOptions{Active: &inactive, TenantID: &tenantID})
	if err != nil {
		return errors.WithStack(err)
	}

	orgNames, err := h.usersOrgNames(ctx, users)
	if err != nil {
		return errors.WithStack(err)
	}

	vmodel.Users = users
	vmodel.CurrentPage = page + 1 // Convert back to 1-based
	vmodel.PageSize = limit
	vmodel.TotalUsers = int(total)
	vmodel.InactiveUsers = int(inactiveCount)
	vmodel.Search = search
	vmodel.OrgNames = orgNames

	return nil
}

// usersOrgNames resolves the organisations of the users on the current page.
//
// One query per row, but the page holds ten of them and the alternative — a
// cross-user membership listing — has no port for it. Should the page size grow,
// this is the call to batch.
func (h *Handler) usersOrgNames(ctx context.Context, users []model.User) (map[model.UserID][]string, error) {
	names := make(map[model.UserID][]string, len(users))

	for _, user := range users {
		memberships, err := h.orgStore.GetUserMemberships(ctx, user.ID())
		if err != nil {
			return nil, errors.WithStack(err)
		}

		for _, membership := range memberships {
			org := membership.Org()
			if org == nil {
				continue
			}
			names[user.ID()] = append(names[user.ID()], org.Name())
		}
	}

	return names, nil
}

func (h *Handler) fillEditUserPageVModelAppLayout(ctx context.Context, vmodel *component.EditUserPageVModel, r *http.Request) error {
	user := httpCtx.User(ctx)
	if user == nil {
		return errors.New("could not retrieve user from context")
	}

	isAdmin := slices.Contains(user.Roles(), authz.RoleAdmin)

	vmodel.AppLayoutVModel = commonComp.AppLayoutVModel{
		User:         user,
		IsAdmin:      isAdmin,
		SelectedItem: "users",
		Breadcrumbs: []commonComp.BreadcrumbItem{
			{Label: "Plateforme", Href: "/admin/"},
			{Label: "Utilisateurs", Href: "/admin/users"},
		},
		Context: commonComp.ContextPlatform,
	}

	return nil
}

func (h *Handler) fillEditUserPageVModelUser(ctx context.Context, vmodel *component.EditUserPageVModel, r *http.Request) error {
	userID := model.UserID(r.PathValue("id"))

	user, err := h.userStore.GetUserByID(ctx, userID)
	if err != nil {
		return errors.WithStack(err)
	}

	vmodel.User = user
	vmodel.AppLayoutVModel.Breadcrumbs = append(vmodel.AppLayoutVModel.Breadcrumbs,
		commonComp.BreadcrumbItem{Label: user.Email(), Href: ""},
	)

	return nil
}

func (h *Handler) fillEditUserPageVModelForm(ctx context.Context, vmodel *component.EditUserPageVModel, r *http.Request) error {
	form := h.newUserForm()

	// Pre-populate form with existing values
	if vmodel.User != nil {
		// Set selected roles
		form.SetFieldValues("roles", vmodel.User.Roles()...)
		// Set active status
		form.SetFieldValues("active", strconv.FormatBool(vmodel.User.Active()))
	}

	vmodel.UserForm = form
	return nil
}

func (h *Handler) newUserForm() *form.Form {
	// Available roles in the system
	availableRoles := []struct {
		Label string
		Value string
	}{
		{Label: "Utilisateur", Value: authz.RoleUser},
		{Label: "Administrateur", Value: authz.RoleAdmin},
	}

	form := formx.New([]form.Field{
		formx.NewField("roles",
			formx.WithLabel("Rôles"),
			formx.WithDescription("Rôles assignés à l'utilisateur"),
			formx.WithType("select"),
			formx.WithSelectOptions(slices.Collect(func(yield func(formx.SelectOption) bool) {
				for _, role := range availableRoles {
					if !yield(formx.SelectOption{
						Label: role.Label,
						Value: role.Value,
					}) {
						return
					}
				}
			})...),
		),
		formx.NewField("active",
			formx.WithLabel("Statut"),
			formx.WithDescription("Statut d'activation de l'utilisateur"),
			formx.WithType("select"),
			formx.WithRequired(true),
			formx.WithSelectOptions(
				formx.SelectOption{
					Label: "Actif",
					Value: "true",
				},
				formx.SelectOption{
					Label: "Inactif",
					Value: "false",
				},
			),
		),
	}, form.WithDefaultRenderer(templui.NewFieldRenderer()))

	return form
}
