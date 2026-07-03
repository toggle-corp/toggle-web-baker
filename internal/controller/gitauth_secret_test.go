package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func gitAuthSecret(namespace, name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       data,
	}
}

func fakeReader(t *testing.T, objs ...client.Object) client.Reader {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
}

// Disabled gitAuth means there is nothing to validate: nil regardless of client.
func TestValidateGitAuthSecret_DisabledNoError(t *testing.T) {
	c := fakeReader(t)
	if err := ValidateGitAuthSecret(context.Background(), c, "baker-system", GitAuth{}); err != nil {
		t.Fatalf("disabled gitAuth must not error, got %v", err)
	}
}

// A present Secret with both non-empty keys validates.
func TestValidateGitAuthSecret_PresentAndComplete(t *testing.T) {
	c := fakeReader(t, gitAuthSecret("baker-system", "baker-git-credential", map[string][]byte{
		"username": []byte("bot"),
		"password": []byte("tok"),
	}))
	ga := GitAuth{SecretName: "baker-git-credential", Hosts: []string{"github.com"}}
	if err := ValidateGitAuthSecret(context.Background(), c, "baker-system", ga); err != nil {
		t.Fatalf("complete secret must validate, got %v", err)
	}
}

// A missing Secret is a hard error naming the Secret (not its data).
func TestValidateGitAuthSecret_MissingSecret(t *testing.T) {
	c := fakeReader(t)
	ga := GitAuth{SecretName: "baker-git-credential", Hosts: []string{"github.com"}}
	err := ValidateGitAuthSecret(context.Background(), c, "baker-system", ga)
	if err == nil {
		t.Fatal("missing secret must error")
	}
	if !strings.Contains(err.Error(), "baker-git-credential") {
		t.Fatalf("error must name the Secret, got %v", err)
	}
}

// An incomplete Secret (missing or empty username/password) is a hard error, and
// the error never contains the secret VALUES.
func TestValidateGitAuthSecret_IncompleteKeys(t *testing.T) {
	cases := map[string]map[string][]byte{
		"missing password": {"username": []byte("bot")},
		"missing username": {"password": []byte("tok")},
		"empty username":   {"username": []byte(""), "password": []byte("tok")},
		"empty password":   {"username": []byte("bot"), "password": []byte("")},
		"no keys":          {},
	}
	for name, data := range cases {
		c := fakeReader(t, gitAuthSecret("baker-system", "baker-git-credential", data))
		ga := GitAuth{SecretName: "baker-git-credential", Hosts: []string{"github.com"}}
		err := ValidateGitAuthSecret(context.Background(), c, "baker-system", ga)
		if err == nil {
			t.Errorf("%s: expected error, got nil", name)
			continue
		}
		if strings.Contains(err.Error(), "tok") || strings.Contains(err.Error(), "bot") {
			t.Errorf("%s: error must not leak secret values, got %v", name, err)
		}
	}
}
