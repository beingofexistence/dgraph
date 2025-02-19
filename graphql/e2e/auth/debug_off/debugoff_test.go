//go:build integration

package debugoff

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"

	"github.com/dgraph-io/dgraph/graphql/authorization"
	"github.com/dgraph-io/dgraph/graphql/e2e/common"
	"github.com/dgraph-io/dgraph/testutil"
	"github.com/dgraph-io/dgraph/x"
)

var (
	metaInfo *testutil.AuthMeta
)

type TestCase struct {
	user      string
	role      string
	result    string
	variables map[string]interface{}
}

func TestAddGQL(t *testing.T) {
	testCases := []TestCase{{
		user:   "user1",
		result: `{"addUserSecret":{"usersecret":[{"aSecret":"secret1"}]}}`,
		variables: map[string]interface{}{"user": &common.UserSecret{
			ASecret: "secret1",
			OwnedBy: "user1",
		}},
	}, {
		user:   "user2",
		result: ``,
		variables: map[string]interface{}{"user": &common.UserSecret{
			ASecret: "secret2",
			OwnedBy: "user1",
		}},
	}}

	query := `
		mutation addUser($user: AddUserSecretInput!) {
			addUserSecret(input: [$user]) {
				userSecret {
					aSecret
				}
			}
		}
	`
	var expected, result struct {
		AddUserSecret struct {
			UserSecret []*common.UserSecret
		}
	}

	for _, tcase := range testCases {
		getUserParams := &common.GraphQLParams{
			Headers:   common.GetJWT(t, tcase.user, tcase.role, metaInfo),
			Query:     query,
			Variables: tcase.variables,
		}
		gqlResponse := getUserParams.ExecuteAsPost(t, common.GraphqlURL)
		if tcase.result == "" {
			require.Equal(t, len(gqlResponse.Errors), 0)
			continue
		}

		common.RequireNoGQLErrors(t, gqlResponse)
		require.NoError(t, json.Unmarshal([]byte(tcase.result), &expected))
		require.NoError(t, json.Unmarshal([]byte(gqlResponse.Data), &result))

		opt := cmpopts.IgnoreFields(common.UserSecret{}, "Id")
		if diff := cmp.Diff(expected, result, opt); diff != "" {
			t.Errorf("result mismatch (-want +got):\n%s", diff)
		}

		for _, i := range result.AddUserSecret.UserSecret {
			i.Delete(t, tcase.user, tcase.role, metaInfo)
		}
	}
}

func TestAddMutationWithXid(t *testing.T) {
	mutation := `
	mutation addTweets($tweet: AddTweetsInput!){
      addTweets(input: [$tweet]) {
        numUids
      }
    }
	`

	tweet := common.Tweets{
		Id:        "tweet1",
		Text:      "abc",
		Timestamp: "2020-10-10",
	}
	user := "foo"
	addTweetsParams := &common.GraphQLParams{
		Headers:   common.GetJWT(t, user, "", metaInfo),
		Query:     mutation,
		Variables: map[string]interface{}{"tweet": tweet},
	}

	// Add the tweet for the first time.
	gqlResponse := addTweetsParams.ExecuteAsPost(t, common.GraphqlURL)
	common.RequireNoGQLErrors(t, gqlResponse)

	// Re-adding the tweet should fail.
	gqlResponse = addTweetsParams.ExecuteAsPost(t, common.GraphqlURL)
	common.RequireNoGQLErrors(t, gqlResponse)

	// Clear the tweet.
	tweet.DeleteByID(t, user, metaInfo)
}

func TestAddMutationWithAuthOnIDFieldHavingInterfaceArg(t *testing.T) {
	// add Library Member
	addLibraryMemberParams := &common.GraphQLParams{
		Query: `mutation addLibraryMember($input: [AddLibraryMemberInput!]!) {
                         addLibraryMember(input: $input, upsert: false) {
                          numUids
                         }
                        }`,
		Variables: map[string]interface{}{"input": []interface{}{
			map[string]interface{}{
				"refID":     "101",
				"name":      "Alice",
				"readHours": "4d2hr",
			}},
		},
	}

	gqlResponse := addLibraryMemberParams.ExecuteAsPost(t, common.GraphqlURL)
	common.RequireNoGQLErrors(t, gqlResponse)

	// add SportsMember should return error but in debug mode
	// because interface type have auth rules defined on it
	var resultLibraryMember struct {
		AddLibraryMember struct {
			NumUids int
		}
	}
	err := json.Unmarshal(gqlResponse.Data, &resultLibraryMember)
	require.NoError(t, err)
	require.Equal(t, 1, resultLibraryMember.AddLibraryMember.NumUids)

	addSportsMemberParams := &common.GraphQLParams{
		Query: `mutation addSportsMember($input: [AddSportsMemberInput!]!) {
                         addSportsMember(input: $input, upsert: false) {
                          numUids
                         }
                        }`,
		Variables: map[string]interface{}{"input": []interface{}{
			map[string]interface{}{
				"refID": "101",
				"name":  "Bob",
				"plays": "football and cricket",
			}},
		},
	}

	gqlResponse = addSportsMemberParams.ExecuteAsPost(t, common.GraphqlURL)
	common.RequireNoGQLErrors(t, gqlResponse)
	var resultSportsMember struct {
		AddSportsMember struct {
			NumUids int
		}
	}
	err = json.Unmarshal(gqlResponse.Data, &resultSportsMember)
	require.NoError(t, err)
	// We show no error here as it could be a security violation
	require.Equal(t, 0, resultSportsMember.AddSportsMember.NumUids)

	// cleanup
	common.DeleteGqlType(t, "LibraryMember", map[string]interface{}{}, 1, nil)
}

func TestMain(m *testing.M) {
	schemaFile := "../schema.graphql"
	schema, err := os.ReadFile(schemaFile)
	x.Panic(err)

	jsonFile := "../test_data.json"
	data, err := os.ReadFile(jsonFile)
	if err != nil {
		panic(errors.Wrapf(err, "Unable to read file %s.", jsonFile))
	}

	jwtAlgo := []string{jwt.SigningMethodHS256.Name, jwt.SigningMethodRS256.Name}
	for _, algo := range jwtAlgo {
		authSchema, err := testutil.AppendAuthInfo(schema, algo, "../sample_public_key.pem", false)
		x.Panic(err)

		authMeta, err := authorization.Parse(string(authSchema))
		x.Panic(err)

		metaInfo = &testutil.AuthMeta{
			PublicKey:      authMeta.VerificationKey,
			Namespace:      authMeta.Namespace,
			Algo:           authMeta.Algo,
			Header:         authMeta.Header,
			PrivateKeyPath: "../sample_private_key.pem",
		}

		common.BootstrapServer(authSchema, data)
		// Data is added only in the first iteration, but the schema is added every iteration.
		if data != nil {
			data = nil
		}
		exitCode := m.Run()
		if exitCode != 0 {
			os.Exit(exitCode)
		}
	}
	os.Exit(0)
}
