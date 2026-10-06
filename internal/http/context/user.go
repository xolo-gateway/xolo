package context

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

const keyUser contextKey = "user"

func User(ctx context.Context) model.User {
	user, ok := ctx.Value(keyUser).(model.User)
	if !ok {
		return nil
	}

	return user
}

func SetUser(ctx context.Context, user model.User) context.Context {
	actor := model.ActorFromContext(ctx)
	if user != nil {
		actor.UserID = user.ID()
	} else {
		actor.UserID = ""
	}
	return context.WithValue(model.WithActor(ctx, actor), keyUser, user)
}
