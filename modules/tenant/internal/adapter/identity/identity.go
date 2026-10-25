// Package identity はスタッフの認証基盤（Cognito）の操作（business-console の設計 §2・§5.3）。tenant の app は Provider だけを見る。
package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	cip "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"

	"subscription-platform/modules/tenant/internal/domain"
)

// ErrUnavailable は認証基盤への要求が失敗した（503 identity_provider_unavailable）。
var ErrUnavailable = errors.New("tenant: identity provider unavailable")

// Provider はスタッフのアカウントの操作のポート。
type Provider interface {
	// EnsureUser はメールアドレスのアカウントを探し、なければ作って招待メール（仮パスワード）を送る。subject（sub）と状態を返す。
	EnsureUser(ctx context.Context, email, displayName string) (subject string, state domain.AccountState, err error)
	// ResendInvitation は仮パスワードを作り直して招待メールを送り直す。アカウントがすでに有効なら state=active を返す（再送しない）。
	ResendInvitation(ctx context.Context, email string) (domain.AccountState, error)
}

// Cognito はスタッフのユーザープールの管理 API（AdminGetUser・AdminCreateUser）を使う。
type Cognito struct {
	Client     *cip.Client
	UserPoolID string
}

func (c Cognito) EnsureUser(ctx context.Context, email, displayName string) (string, domain.AccountState, error) {
	sub, state, err := c.get(ctx, email)
	if err == nil {
		return sub, state, nil
	}
	var nf *types.UserNotFoundException
	if !errors.As(err, &nf) {
		return "", "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	out, err := c.Client.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId: aws.String(c.UserPoolID), Username: aws.String(email), DesiredDeliveryMediums: []types.DeliveryMediumType{types.DeliveryMediumTypeEmail},
		UserAttributes: []types.AttributeType{
			{Name: aws.String("email"), Value: aws.String(email)},
			{Name: aws.String("email_verified"), Value: aws.String("true")}, // 招待したアドレスに届いた仮パスワードで入るので確認済みとする
			{Name: aws.String("name"), Value: aws.String(displayName)},
		},
	})
	if err != nil {
		var exists *types.UsernameExistsException
		if errors.As(err, &exists) { // 同時に招待された
			return c.get(ctx, email)
		}
		return "", "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return subOf(out.User.Attributes), domain.AccountCreated, nil
}

func (c Cognito) get(ctx context.Context, email string) (string, domain.AccountState, error) {
	u, err := c.Client.AdminGetUser(ctx, &cip.AdminGetUserInput{UserPoolId: aws.String(c.UserPoolID), Username: aws.String(email)})
	if err != nil {
		return "", "", err //nolint:wrapcheck // 呼び出し元が種類で分ける
	}
	state := domain.AccountActive
	if u.UserStatus == types.UserStatusTypeForceChangePassword {
		state = domain.AccountPending
	}
	return subOf(u.UserAttributes), state, nil
}

func (c Cognito) ResendInvitation(ctx context.Context, email string) (domain.AccountState, error) {
	_, state, err := c.get(ctx, email)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if state == domain.AccountActive {
		return state, nil
	}
	_, err = c.Client.AdminCreateUser(ctx, &cip.AdminCreateUserInput{
		UserPoolId: aws.String(c.UserPoolID), Username: aws.String(email), MessageAction: types.MessageActionTypeResend,
		DesiredDeliveryMediums: []types.DeliveryMediumType{types.DeliveryMediumTypeEmail},
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return domain.AccountPending, nil
}

func subOf(attrs []types.AttributeType) string {
	for _, a := range attrs {
		if aws.ToString(a.Name) == "sub" {
			return aws.ToString(a.Value)
		}
	}
	return ""
}

// Dev は Cognito を使わない環境（AUTH_MODE=dev で Cognito のプールを設定していない）の実装。メールは送らず、
// subject はメールアドレスから決まる値にする（開発用 JWT の発行で同じ subject を指定できる）。
type Dev struct{}

// DevSubject はメールアドレスに対応する開発用の subject。
func DevSubject(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(email)))
	return "dev-" + hex.EncodeToString(sum[:12])
}

func (Dev) EnsureUser(_ context.Context, email, _ string) (string, domain.AccountState, error) {
	return DevSubject(email), domain.AccountCreated, nil
}

func (Dev) ResendInvitation(context.Context, string) (domain.AccountState, error) {
	return domain.AccountPending, nil
}
