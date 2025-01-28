package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cello-proj/cello/internal/types"
	"github.com/cello-proj/cello/service/internal/credentials"
	"github.com/cello-proj/cello/service/internal/db"
	"github.com/cello-proj/cello/service/internal/env"
	"github.com/cello-proj/cello/service/internal/workflow"
	th "github.com/cello-proj/cello/service/test/testhelpers"

	"github.com/go-kit/log"
	"github.com/stretchr/testify/assert"
	upper "github.com/upper/db/v4"
)

const (
	// #nosec
	testPassword      = "D34DB33FD34DB33FD34DB33FD34DB33F"
	userAuthHeader    = "vault:user:" + testPassword
	invalidAuthHeader = "bad auth header"
	adminAuthHeader   = "vault:admin:" + testPassword

	workflowResponse = "wf-123456"
)

type test struct {
	name       string
	req        interface{}
	want       int
	body       string
	respFile   string
	authHeader string
	url        string
	method     string
	cpMock     *th.CredsProviderMock
	dbMock     *th.DBClientMock
	gitMock    *th.GitClientMock
	wfMock     *th.WorkflowMock
}

func TestCreateProject(t *testing.T) {
	tests := []test{
		{
			name:       "fails to create project when not admin",
			req:        loadJSON(t, "TestCreateProject/fails_to_create_project_when_not_admin_request.json"),
			want:       http.StatusUnauthorized,
			authHeader: userAuthHeader,
			respFile:   "TestCreateProject/fails_to_create_project_when_not_admin_response.json",
			url:        "/projects",
			method:     "POST",
		},
		{
			name:       "can create project",
			req:        loadJSON(t, "TestCreateProject/can_create_project_request.json"),
			want:       http.StatusOK,
			respFile:   "TestCreateProject/can_create_project_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
				CreateProjectFunc: func(s string) (types.Token, error) {
					return types.Token{
						CreatedAt: "createdAt",
						ExpiresAt: "expiresAt",
						ProjectID: "project1",
						ProjectToken: types.ProjectToken{
							ID: "secret-id-accessor",
						},
						RoleID: "role-id",
						Secret: "secret",
					}, nil
				},
			},
			dbMock: &th.DBClientMock{
				CreateProjectEntryFunc: func(ctx context.Context, pe db.ProjectEntry) error { return nil },
				CreateTokenEntryFunc:   func(ctx context.Context, token types.Token) error { return nil },
			},
		},
		{
			name:       "bad request",
			req:        loadJSON(t, "TestCreateProject/bad_request.json"),
			want:       http.StatusBadRequest,
			respFile:   "TestCreateProject/bad_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects",
			method:     "POST",
		},
		{
			name:       "project name cannot already exist",
			req:        loadJSON(t, "TestCreateProject/project_name_cannot_already_exist.json"),
			want:       http.StatusBadRequest,
			authHeader: adminAuthHeader,
			url:        "/projects",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "project fails to create db entry",
			req:        loadJSON(t, "TestCreateProject/project_fails_to_create_dbentry.json"),
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			url:        "/projects",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
			dbMock: &th.DBClientMock{
				CreateProjectEntryFunc: func(ctx context.Context, pe db.ProjectEntry) error { return errors.New("db error") },
			},
		},
		{
			name:       "project fails to create token entry",
			req:        loadJSON(t, "TestCreateProject/project_fails_to_create_token_entry.json"),
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			url:        "/projects",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				CreateProjectFunc: func(s string) (types.Token, error) {
					return types.Token{
						CreatedAt: "createdAt",
						ExpiresAt: "expiresAt",
						ProjectID: "project1",
						ProjectToken: types.ProjectToken{
							ID: "secret-id-accessor",
						},
						RoleID: "role-id",
						Secret: "secret",
					}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
			dbMock: &th.DBClientMock{
				CreateProjectEntryFunc: func(ctx context.Context, pe db.ProjectEntry) error { return nil },
				CreateTokenEntryFunc:   func(ctx context.Context, token types.Token) error { return errors.New("db error") },
			},
		},
	}
	runTests(t, tests)
}

func TestCreateToken(t *testing.T) {
	tests := []test{
		{
			name:       "can create token",
			req:        loadJSON(t, "TestCreateToken/request.json"),
			want:       http.StatusOK,
			respFile:   "TestCreateToken/can_create_token_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/undeletableprojecttargets/tokens",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				CreateTokenFunc: func(s string) (types.Token, error) {
					return types.Token{
						CreatedAt: "2022-06-21T14:56:10.341066-07:00",
						ExpiresAt: "2023-06-21T14:56:10.341066-07:00",
						ProjectID: "project1",
						ProjectToken: types.ProjectToken{
							ID: "secret-id-accessor",
						},
						RoleID: "role-id",
						Secret: "secret",
					}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				CreateTokenEntryFunc: func(ctx context.Context, t types.Token) error { return nil },
				ListTokenEntriesFunc: func(ctx context.Context, p string) ([]db.TokenEntry, error) {
					return []db.TokenEntry{{
						CreatedAt: "2022-06-21T14:56:10.341066-07:00",
						ExpiresAt: "2023-06-21T14:56:10.341066-07:00",
						ProjectID: "project1",
						TokenID:   "secret-id-accessor",
					}}, nil
				},
				ReadProjectEntryFunc: func(ctx context.Context, p string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1", Repository: "repo"}, nil
				},
			},
		},
		{
			name:       "project does not exist",
			req:        loadJSON(t, "TestCreateToken/request.json"),
			want:       http.StatusNotFound,
			respFile:   "TestCreateToken/project_does_not_exist.json",
			authHeader: adminAuthHeader,
			url:        "/projects/project1234/tokens",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "fails to create token when not admin",
			req:        loadJSON(t, "TestCreateToken/request.json"),
			want:       http.StatusUnauthorized,
			respFile:   "TestCreateToken/fails_to_create_token_when_not_admin_response.json",
			authHeader: userAuthHeader,
			url:        "/projects/undeletableprojecttargets/tokens",
			method:     "POST",
		},
		{
			name:       "token fails to create db entry",
			req:        loadJSON(t, "TestCreateToken/request.json"),
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			url:        "/projects/tokendberror/tokens",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				CreateTokenFunc:   func(s string) (types.Token, error) { return types.Token{}, errors.New("error") },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ListTokenEntriesFunc: func(ctx context.Context, p string) ([]db.TokenEntry, error) {
					return []db.TokenEntry{{
						CreatedAt: "2022-06-21T14:56:10.341066-07:00",
						ExpiresAt: "2023-06-21T14:56:10.341066-07:00",
						ProjectID: "project1",
						TokenID:   "secret-id-accessor",
					}}, nil
				},
				ReadProjectEntryFunc: func(ctx context.Context, p string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1", Repository: "repo"}, nil
				},
			},
		},
		{
			name:       "allowed tokens limit reached",
			req:        loadJSON(t, "TestCreateToken/request.json"),
			want:       http.StatusInternalServerError,
			respFile:   "TestCreateToken/token_limit_reached_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectlisttokenslimit/tokens",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ListTokenEntriesFunc: func(ctx context.Context, p string) ([]db.TokenEntry, error) {
					return []db.TokenEntry{{
						CreatedAt: "2022-06-21T14:56:10.341066-07:00",
						ExpiresAt: "2023-06-21T14:56:10.341066-07:00",
						ProjectID: "project1",
						TokenID:   "secret-id-accessor",
					}, {
						CreatedAt: "2022-07-21T14:00:00.000000-07:00",
						ExpiresAt: "2023-07-21T14:00:00.000000-07:00",
						ProjectID: "project1",
						TokenID:   "secret-id-accessor",
					}}, nil
				},
				ReadProjectEntryFunc: func(ctx context.Context, p string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1", Repository: "repo"}, nil
				},
			},
		},
		{
			name:       "error listing tokens",
			req:        loadJSON(t, "TestCreateToken/request.json"),
			want:       http.StatusInternalServerError,
			respFile:   "TestCreateToken/error_listing_tokens_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectlisttokenserror/tokens",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ListTokenEntriesFunc: func(ctx context.Context, p string) ([]db.TokenEntry, error) {
					return []db.TokenEntry{}, errors.New("error")
				},
				ReadProjectEntryFunc: func(ctx context.Context, p string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1", Repository: "repo"}, nil
				},
			},
		},
	}
	runTests(t, tests)
}

func TestGetTarget(t *testing.T) {
	tests := []test{
		{
			name:       "fails to get target when not admin",
			want:       http.StatusUnauthorized,
			respFile:   "TestGetTarget/fails_to_get_target_when_not_admin_response.json",
			authHeader: userAuthHeader,
			url:        "/projects/undeletableprojecttargets/targets/target1",
			method:     "GET",
		},
		{
			name:       "can get target",
			want:       http.StatusOK,
			respFile:   "TestGetTarget/can_get_target_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/undeletableprojecttargets/targets/TARGET_EXISTS",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				GetTargetFunc: func(s1, s2 string) (types.Target, error) {
					return types.Target{
						Name: "TARGET",
						Properties: types.TargetProperties{
							CredentialType: "assumed_role",
							PolicyArns:     []string{"arn:aws:iam::012345678901:policy/test-policy"},
							PolicyDocument: "{ \"Version\": \"2012-10-17\", \"Statement\": [ { \"Effect\": \"Allow\", \"Action\": \"s3:ListBuckets\", \"Resource\": \"*\" } ] }",
							RoleArn:        "arn:aws:iam::012345678901:role/test-role",
						},
						Type: "aws_account",
					}, nil
				},
				TargetExistsFunc: func(s1, s2 string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "target does not exist",
			want:       http.StatusNotFound,
			respFile:   "TestGetTarget/target_does_not_exist_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/undeletableprojecttargets/targets/targetdoesnotexist",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				TargetExistsFunc: func(s1, s2 string) (bool, error) { return false, nil },
			},
		},
	}
	runTests(t, tests)
}

func TestListTargets(t *testing.T) {
	tests := []test{
		{
			name:       "fails to list targets when not admin",
			want:       http.StatusUnauthorized,
			respFile:   "TestListTargets/fails_to_list_targets_when_not_admin_response.json",
			authHeader: userAuthHeader,
			url:        "/projects/undeletableprojecttargets/targets",
			method:     "GET",
		},
		{
			name:       "can list targets",
			want:       http.StatusOK,
			respFile:   "TestListTargets/can_get_target_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/undeletableprojecttargets/targets",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ListTargetsFunc: func(s string) ([]string, error) {
					return []string{"target1", "target2", "undeletabletarget"}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "project not found",
			want:       http.StatusNotFound,
			respFile:   "TestListTargets/project_not_found_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/badproject/targets",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "no targets",
			want:       http.StatusOK,
			respFile:   "TestListTargets/no_targets_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ListTargetsFunc: func(s string) ([]string, error) {
					return []string{}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
		},
	}
	runTests(t, tests)
}

func TestDeleteProject(t *testing.T) {
	tests := []test{
		{
			name:       "fails to delete project when not admin",
			want:       http.StatusUnauthorized,
			authHeader: userAuthHeader,
			url:        "/projects/projectalreadyexists",
			method:     "DELETE",
		},
		{
			name:       "can delete project",
			want:       http.StatusOK,
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteProjectFunc: func(s string) error { return nil },
				ListTargetsFunc:   func(s string) ([]string, error) { return []string{}, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				DeleteProjectEntryFunc: func(ctx context.Context, project string) error { return nil },
			},
		},
		{
			name:       "fails to delete project if any targets exist",
			want:       http.StatusBadRequest,
			authHeader: adminAuthHeader,
			url:        "/projects/undeletableprojecttargets",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				ListTargetsFunc:   func(s string) ([]string, error) { return []string{"target"}, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "fails to delete project",
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			url:        "/projects/undeletableproject",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteProjectFunc: func(s string) error { return errors.New("cp error") },
				ListTargetsFunc:   func(s string) ([]string, error) { return []string{}, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "fails to delete project db entry",
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			url:        "/projects/somedeletedberror",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteProjectFunc: func(s string) error { return nil },
				ListTargetsFunc:   func(s string) ([]string, error) { return []string{}, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				DeleteProjectEntryFunc: func(ctx context.Context, project string) error { return errors.New("error") },
			},
		},
	}
	runTests(t, tests)
}

func TestGetProject(t *testing.T) {
	tests := []test{
		{
			name:       "cannot get project, when not admin",
			want:       http.StatusUnauthorized,
			authHeader: userAuthHeader,
			method:     "GET",
			url:        "/projects/project1",
		},
		{
			name:       "project exists, successful get project",
			want:       http.StatusOK,
			authHeader: adminAuthHeader,
			method:     "GET",
			url:        "/projects/project1",
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1", Repository: "repo"}, nil
				},
			},
		},
		{
			name:       "project does not exist",
			want:       http.StatusNotFound,
			authHeader: adminAuthHeader,
			method:     "GET",
			url:        "/projects/projectdoesnotexist",
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{}, upper.ErrNoMoreRows
				},
			},
		},
	}
	runTests(t, tests)
}

func TestCreateTarget(t *testing.T) {
	tests := []test{
		{
			name:       "can create target",
			req:        loadJSON(t, "TestCreateTarget/can_create_target_request.json"),
			want:       http.StatusOK,
			respFile:   "TestCreateTarget/can_create_target_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				CreateTargetFunc:  func(s string, target types.Target) error { return nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "fails to create target when not admin",
			req:        loadJSON(t, "TestCreateTarget/fails_to_create_target_when_not_admin_request.json"),
			want:       http.StatusUnauthorized,
			respFile:   "TestCreateTarget/fails_to_create_target_when_not_admin_response.json",
			authHeader: userAuthHeader,
			url:        "/projects/projectalreadyexists/targets",
			method:     "POST",
		},
		{
			name:       "fails to create target when using a bad auth header",
			req:        loadJSON(t, "TestCreateTarget/can_create_target_request.json"),
			want:       http.StatusUnauthorized,
			respFile:   "TestCreateTarget/fails_to_create_target_when_bad_auth_header_response.json",
			authHeader: invalidAuthHeader,
			url:        "/projects/projectalreadyexists/targets",
			method:     "POST",
		},
		{
			name:       "bad request",
			req:        loadJSON(t, "TestCreateTarget/bad_request.json"),
			want:       http.StatusBadRequest,
			respFile:   "TestCreateTarget/bad_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets",
			method:     "POST",
		},
		{
			name:       "target name cannot already exist",
			req:        loadJSON(t, "TestCreateTarget/target_name_cannot_already_exist_request.json"),
			want:       http.StatusBadRequest,
			respFile:   "TestCreateTarget/target_name_cannot_already_exist_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "project must exist",
			req:        loadJSON(t, "TestCreateTarget/project_must_exist_request.json"),
			want:       http.StatusBadRequest,
			respFile:   "TestCreateTarget/project_must_exist_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectdoesnotexist/targets",
			method:     "POST",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
		},
	}
	runTests(t, tests)
}

func TestDeleteTarget(t *testing.T) {
	tests := []test{
		{
			name:       "fails to delete target when not admin",
			want:       http.StatusUnauthorized,
			authHeader: userAuthHeader,
			url:        "/projects/projectalreadyexists/targets/target1",
			method:     "DELETE",
		},
		{
			name:       "fails to delete target when using a bad auth header",
			want:       http.StatusUnauthorized,
			authHeader: invalidAuthHeader,
			url:        "/projects/projectalreadyexists/targets/target1",
			method:     "DELETE",
		},
		{
			name:       "can delete target",
			want:       http.StatusOK,
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/target1",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteTargetFunc: func(s1, s2 string) error { return nil },
				TargetExistsFunc: func(s1, s2 string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "target fails to delete",
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/undeletabletarget",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteTargetFunc: func(s1, s2 string) error { return errors.New("error") },
				TargetExistsFunc: func(s1, s2 string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "error retrieving target",
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/errorretrivingtarget",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				TargetExistsFunc: func(s1 string, s2 string) (bool, error) { return false, errors.New("error") },
			},
		},
		{
			name:       "target does not exist",
			want:       http.StatusNotFound,
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/targetdoesnotexist",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteTargetFunc: func(s1, s2 string) error { return errors.New("error") },
				TargetExistsFunc: func(s1 string, s2 string) (bool, error) { return false, nil },
			},
		},
	}
	runTests(t, tests)
}

func TestUpdateTarget(t *testing.T) {
	tests := []test{
		{
			name:       "can update target",
			req:        loadJSON(t, "TestUpdateTarget/can_update_target_request.json"),
			want:       http.StatusOK,
			respFile:   "TestUpdateTarget/can_update_target_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/TARGET_EXISTS",
			method:     "PATCH",
			cpMock: &th.CredsProviderMock{
				GetTargetFunc: func(s1, s2 string) (types.Target, error) {
					return types.Target{
						Name: "TARGET_EXISTS",
						Properties: types.TargetProperties{
							CredentialType: "assumed_role",
							PolicyArns:     []string{},
							PolicyDocument: "policyDoc",
							RoleArn:        "roleARN",
						},
						Type: "aws_account",
					}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return true, nil },
				UpdateTargetFunc:  func(s string, target types.Target) error { return nil },
			},
		},
		{
			name:       "fails to update target when not admin",
			req:        loadJSON(t, "TestUpdateTarget/fails_to_update_target_when_not_admin_request.json"),
			want:       http.StatusUnauthorized,
			respFile:   "TestUpdateTarget/fails_to_update_target_when_not_admin_response.json",
			authHeader: userAuthHeader,
			url:        "/projects/projectalreadyexists/targets/TARGET_EXISTS",
			method:     "PATCH",
		},
		{
			name:       "fails to update target when using a bad auth header",
			req:        loadJSON(t, "TestUpdateTarget/fails_to_update_target_when_not_admin_request.json"),
			want:       http.StatusUnauthorized,
			respFile:   "TestUpdateTarget/fails_to_update_target_when_bad_auth_header_response.json",
			authHeader: invalidAuthHeader,
			url:        "/projects/projectalreadyexists/targets/TARGET_EXISTS",
			method:     "PATCH",
		},
		{
			name:       "fails to update target credential_type",
			req:        loadJSON(t, "TestUpdateTarget/fails_to_update_credential_type_request.json"),
			want:       http.StatusBadRequest,
			respFile:   "TestUpdateTarget/fails_to_update_credential_type_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/TARGET_EXISTS",
			method:     "PATCH",
			cpMock: &th.CredsProviderMock{
				GetTargetFunc: func(s1, s2 string) (types.Target, error) {
					return types.Target{
						Name: "TARGET_EXISTS",
						Properties: types.TargetProperties{
							CredentialType: "assumed_role",
							PolicyArns:     []string{"arn:aws:iam::012345678901:policy/test-policy"},
							PolicyDocument: "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:ListBuckets\",\"Resource\":\"*\"}]}",
							RoleArn:        "arn:aws:iam::012345678901:role/test-role",
						},
						Type: "aws_account",
					}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return true, nil },
			},
		},
		{
			name:       "does not overwrite target name or type when in request",
			req:        loadJSON(t, "TestUpdateTarget/fails_to_update_target_name_request.json"),
			want:       http.StatusOK,
			respFile:   "TestUpdateTarget/fails_to_update_target_name_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/TARGET_EXISTS",
			method:     "PATCH",
			cpMock: &th.CredsProviderMock{
				GetTargetFunc: func(s1, s2 string) (types.Target, error) {
					return types.Target{
						Name: "TARGET_EXISTS",
						Properties: types.TargetProperties{
							CredentialType: "assumed_role",
							PolicyArns:     []string{"arn:aws:iam::012345678901:policy/test-policy"},
							PolicyDocument: "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:ListBuckets\",\"Resource\":\"*\"}]}",
							RoleArn:        "arn:aws:iam::012345678901:role/test-role",
						},
						Type: "aws_account",
					}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return true, nil },
				UpdateTargetFunc:  func(s string, target types.Target) error { return nil },
			},
		},
		{
			name:       "target name must exist",
			req:        loadJSON(t, "TestUpdateTarget/target_name_must_exist_request.json"),
			want:       http.StatusNotFound,
			respFile:   "TestUpdateTarget/target_name_must_exist_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectalreadyexists/targets/INVALID_TARGET",
			method:     "PATCH",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "project must exist",
			req:        loadJSON(t, "TestUpdateTarget/project_must_exist_request.json"),
			want:       http.StatusNotFound,
			respFile:   "TestUpdateTarget/project_must_exist_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectdoesnotexist/targets/TARGET_EXISTS",
			method:     "PATCH",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
		},
	}
	runTests(t, tests)
}

func TestCreateWorkflow(t *testing.T) {
	tests := []test{
		{
			name:       "can create workflows",
			req:        loadJSON(t, "TestCreateWorkflow/can_create_workflow_request.json"),
			want:       http.StatusOK,
			authHeader: userAuthHeader,
			respFile:   "TestCreateWorkflow/can_create_workflow_response.json",
			method:     "POST",
			url:        "/workflows",
			cpMock: &th.CredsProviderMock{
				GetTokenFunc:      func() (string, error) { return testPassword, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return true, nil },
			},
			wfMock: &th.WorkflowMock{
				SubmitFunc: func(ctx context.Context, from string, parameters, labels map[string]string) (string, error) {
					return workflowResponse, nil
				},
			},
		},
		// We test this specific validation as it's server side only.
		{
			name:       "framework must be valid",
			req:        loadJSON(t, "TestCreateWorkflow/framework_must_be_valid_request.json"),
			want:       http.StatusBadRequest,
			authHeader: userAuthHeader,
			respFile:   "TestCreateWorkflow/framework_must_be_valid_response.json",
			method:     "POST",
			url:        "/workflows",
		},
		// We test this specific validation as it's server side only.
		{
			name:       "type must be valid",
			respFile:   "TestCreateWorkflow/type_must_be_valid_response.json",
			req:        loadJSON(t, "TestCreateWorkflow/type_must_be_valid_request.json"),
			authHeader: userAuthHeader,
			want:       http.StatusBadRequest,
			method:     "POST",
			url:        "/workflows",
		},
		{
			name:       "project must exist",
			req:        loadJSON(t, "TestCreateWorkflow/project_must_exist.json"),
			authHeader: userAuthHeader,
			want:       http.StatusBadRequest,
			method:     "POST",
			url:        "/workflows",
			cpMock: &th.CredsProviderMock{
				GetTokenFunc:      func() (string, error) { return testPassword, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "target must exist",
			req:        loadJSON(t, "TestCreateWorkflow/target_must_exist.json"),
			authHeader: userAuthHeader,
			want:       http.StatusBadRequest,
			method:     "POST",
			url:        "/workflows",
			cpMock: &th.CredsProviderMock{
				GetTokenFunc:      func() (string, error) { return testPassword, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "cannot create workflow with bad auth header",
			req:        loadJSON(t, "TestCreateWorkflow/can_create_workflow_response.json"),
			want:       http.StatusUnauthorized,
			authHeader: invalidAuthHeader,
			method:     "POST",
			url:        "/workflows",
		},
		// TODO with admin credentials should fail
	}
	runTests(t, tests)
}

func TestCreateWorkflowFromGit(t *testing.T) {
	tests := []test{
		{
			name:       "can create workflows",
			req:        loadJSON(t, "TestCreateWorkflowFromGit/good_request.json"),
			want:       http.StatusOK,
			authHeader: userAuthHeader,
			respFile:   "TestCreateWorkflowFromGit/good_response.json",
			method:     "POST",
			url:        "/projects/project1/targets/target1/operations",
			cpMock: &th.CredsProviderMock{
				GetTokenFunc:      func() (string, error) { return testPassword, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{
						ProjectID:  "project1",
						Repository: "repo",
					}, nil
				},
			},
			gitMock: &th.GitClientMock{
				GetManifestFileFunc: func(repository, commitHash, path string) ([]byte, error) {
					return loadFileBytes("TestCreateWorkflow/can_create_workflow_request.json")
				},
			},
			wfMock: &th.WorkflowMock{
				SubmitFunc: func(ctx context.Context, from string, parameters map[string]string, labels map[string]string) (string, error) {
					return workflowResponse, nil
				},
			},
		},
		{
			name:       "workflows environment variables",
			req:        loadJSON(t, "TestCreateWorkflowFromGit/good_request.json"),
			want:       http.StatusOK,
			authHeader: userAuthHeader,
			respFile:   "TestCreateWorkflowFromGit/good_response.json",
			method:     "POST",
			url:        "/projects/project1/targets/target1/operations",
			cpMock: &th.CredsProviderMock{
				GetTokenFunc:      func() (string, error) { return testPassword, nil },
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
				TargetExistsFunc:  func(s1, s2 string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{
						ProjectID:  "project1",
						Repository: "repo",
					}, nil
				},
			},
			gitMock: &th.GitClientMock{
				GetManifestFileFunc: func(repository, commitHash, path string) ([]byte, error) {
					return loadFileBytes("TestCreateWorkflow/create_workflow_env_variables.json")
				},
			},
			wfMock: &th.WorkflowMock{
				SubmitFunc: func(ctx context.Context, from string, parameters map[string]string, labels map[string]string) (string, error) {
					if !strings.Contains(parameters["environment_variables_string"], "variable_with_single_quote='someones value'") {
						return "", errors.New("failed to quote string with single quote in it")
					}
					if !strings.Contains(parameters["environment_variables_string"], "single_quoted_variable='single_quoted_variable'") {
						return "", errors.New("failed to quote string with single quotes quited it")
					}
					if !strings.Contains(parameters["environment_variables_string"], "double_quoted_variable='double_quoted_variable'") {
						return "", errors.New("failed to quote string with double quotes quited it")
					}
					if !strings.Contains(parameters["environment_variables_string"], "user='first_name last_name'") {
						return "", errors.New("failed to quote string with empty space it")
					}
					if !strings.Contains(parameters["environment_variables_string"], "foobar='barfoo'") {
						return "", errors.New("failed to quote string")
					}
					if !strings.Contains(parameters["environment_variables_string"], "variable_with_double_quote='I love book Harry Potter'") {
						return "", errors.New("failed to quote string with double quote in it")
					}
					return workflowResponse, nil
				},
			},
		},
		{
			name:       "bad request",
			req:        loadJSON(t, "TestCreateWorkflowFromGit/bad_request.json"),
			want:       http.StatusBadRequest,
			authHeader: userAuthHeader,
			respFile:   "TestCreateWorkflowFromGit/bad_response.json",
			method:     "POST",
			url:        "/projects/project1/targets/target1/operations",
		},
		// TODO with admin credentials should fail
	}
	runTests(t, tests)
}

func TestGetWorkflow(t *testing.T) {
	tests := []test{
		{
			name:       "workflow exists, successful get workflow",
			want:       http.StatusOK,
			authHeader: adminAuthHeader,
			method:     "GET",
			url:        "/workflows/WORKFLOW_ALREADY_EXISTS",
			wfMock: &th.WorkflowMock{
				StatusFunc: func(ctx context.Context, workflowName string) (*workflow.Status, error) {
					return &workflow.Status{Status: "success"}, nil
				},
			},
		},
		{
			name:       "workflow does not exist",
			want:       http.StatusNotFound,
			authHeader: adminAuthHeader,
			method:     "GET",
			url:        "/workflows/WORKFLOW_DOES_NOT_EXIST",
			wfMock: &th.WorkflowMock{
				StatusFunc: func(ctx context.Context, workflowName string) (*workflow.Status, error) {
					return &workflow.Status{Status: "failed"}, errors.New("rpc error: code = NotFound desc = workflows.argoproj.io \"WORKFLOW_DOES_NOT_EXIST\" not found")
				},
			},
		},
		{
			name:       "workflow internal error",
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			method:     "GET",
			url:        "/workflows/WORKFLOW_ERROR",
			wfMock: &th.WorkflowMock{
				StatusFunc: func(ctx context.Context, workflowName string) (*workflow.Status, error) {
					return &workflow.Status{Status: "failed"}, errors.New("rpc error: code = workflow error desc = unknown")
				},
			},
		},
	}
	runTests(t, tests)
}

func TestGetWorkflowLogs(t *testing.T) {
	tests := []test{
		{
			// TODO: this should be renamed to logs already exists, and a test case created to
			// test an actual success
			name:       "successful get workflow logs",
			want:       http.StatusOK,
			authHeader: adminAuthHeader,
			method:     "GET",
			url:        "/workflows/WORKFLOW_ALREADY_EXISTS/logs",
			wfMock: &th.WorkflowMock{
				LogsFunc: func(ctx context.Context, workflowName string) (*workflow.Logs, error) { return nil, nil },
			},
		},
		{
			name:       "workflow does not exist",
			want:       http.StatusInternalServerError,
			authHeader: adminAuthHeader,
			method:     "GET",
			url:        "/workflows/WORKFLOW_DOES_NOT_EXIST/logs",
			wfMock: &th.WorkflowMock{
				LogsFunc: func(ctx context.Context, workflowName string) (*workflow.Logs, error) {
					return nil, errors.New("workflow does not exist")
				},
			},
		},
	}
	runTests(t, tests)
}

func TestListWorkflows(t *testing.T) {
	tests := []test{
		{
			name:       "can get workflows",
			want:       http.StatusOK,
			authHeader: userAuthHeader,
			method:     "GET",
			url:        "/projects/projects1/targets/target1/workflows",
			wfMock: &th.WorkflowMock{
				ListStatusFunc: func(ctx context.Context) ([]workflow.Status, error) {
					return []workflow.Status{
						{
							Name:     "project1-target1-abcde",
							Status:   "succeeded",
							Created:  "1658514800",
							Finished: "1658514856",
						},
						{
							Name:     "project2-target2-12345",
							Status:   "succeeded",
							Created:  "1658514764",
							Finished: "1658514793",
						},
					}, nil
				},
			},
		},
		{
			name:       "no workflows",
			want:       http.StatusOK,
			authHeader: userAuthHeader,
			method:     "GET",
			url:        "/projects/projects1/targets/target1/workflows",
			wfMock: &th.WorkflowMock{
				ListStatusFunc: func(ctx context.Context) ([]workflow.Status, error) {
					return []workflow.Status{}, nil
				},
			},
		},
	}
	runTests(t, tests)
}

func TestDeleteToken(t *testing.T) {
	tests := []test{
		{
			name:       "fails to delete tokens when not admin",
			want:       http.StatusUnauthorized,
			respFile:   "TestDeleteToken/fails_to_delete_token_when_not_admin_response.json",
			authHeader: userAuthHeader,
			url:        "/projects/project/tokens/existingtoken",
			method:     "DELETE",
		},
		{
			name:       "can delete token",
			want:       http.StatusOK,
			respFile:   "TestDeleteToken/can_delete_token_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/project/tokens/existingtoken",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				GetProjectTokenFunc: func(s1 string, s2 string) (types.ProjectToken, error) {
					return types.ProjectToken{ID: "1234"}, nil
				},
				ProjectExistsFunc:      func(s string) (bool, error) { return true, nil },
				DeleteProjectTokenFunc: func(p, t string) error { return nil },
			},
			dbMock: &th.DBClientMock{
				DeleteTokenEntryFunc: func(ctx context.Context, token string) error { return nil },
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1"}, nil
				},
				ReadTokenEntryFunc: func(ctx context.Context, token string) (db.TokenEntry, error) {
					return db.TokenEntry{ProjectID: "project1", TokenID: "1234", CreatedAt: "2022-06-21T14:42:50.182037-07:00"}, nil
				},
			},
		},
		{
			name:       "project does not exist",
			want:       http.StatusNotFound,
			respFile:   "TestDeleteToken/project_does_not_exist_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectdoesnotexist/tokens/tokendoesnotexist",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "token does not exist in DB or CP",
			want:       http.StatusOK,
			respFile:   "TestDeleteToken/can_delete_token_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/project/tokens/tokendoesnotexist",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				GetProjectTokenFunc: func(s1 string, s2 string) (types.ProjectToken, error) {
					return types.ProjectToken{}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1"}, nil
				},
				ReadTokenEntryFunc: func(ctx context.Context, token string) (db.TokenEntry, error) {
					return db.TokenEntry{}, nil
				},
			},
		},
		{
			name:       "token exists in CP but not in DB",
			want:       http.StatusOK,
			respFile:   "TestDeleteToken/can_delete_token_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/project/tokens/tokenonlyincp",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteProjectTokenFunc: func(s1, s2 string) error { return nil },
				GetProjectTokenFunc: func(s1 string, s2 string) (types.ProjectToken, error) {
					return types.ProjectToken{ID: "tokenonlyincp"}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1"}, nil
				},
				ReadTokenEntryFunc: func(ctx context.Context, token string) (db.TokenEntry, error) {
					return db.TokenEntry{}, nil
				},
			},
		},
		{
			name:       "token exists in DB but not in CP",
			want:       http.StatusOK,
			respFile:   "TestDeleteToken/can_delete_token_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/project/tokens/tokenonlyindb",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				GetProjectTokenFunc: func(s1 string, s2 string) (types.ProjectToken, error) {
					return types.ProjectToken{}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				DeleteTokenEntryFunc: func(ctx context.Context, token string) error { return nil },
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1"}, nil
				},
				ReadTokenEntryFunc: func(ctx context.Context, token string) (db.TokenEntry, error) {
					return db.TokenEntry{TokenID: "tokenonlyindb"}, nil
				},
			},
		},
		{
			name:       "token delete error",
			want:       http.StatusInternalServerError,
			respFile:   "TestDeleteToken/token_delete_error_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/project/tokens/deletetokenerror",
			method:     "DELETE",
			cpMock: &th.CredsProviderMock{
				DeleteProjectTokenFunc: func(s1, s2 string) error { return errors.New("error deleting token from Vault") },
				GetProjectTokenFunc: func(s1 string, s2 string) (types.ProjectToken, error) {
					return types.ProjectToken{ID: "1234"}, nil
				},
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				DeleteTokenEntryFunc: func(ctx context.Context, token string) error { return errors.New("error deleting entry from DB") },
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1"}, nil
				},
				ReadTokenEntryFunc: func(ctx context.Context, token string) (db.TokenEntry, error) {
					return db.TokenEntry{ProjectID: "project1", TokenID: "1234", CreatedAt: "2022-06-21T14:42:50.182037-07:00"}, nil
				},
			},
		},
	}
	runTests(t, tests)
}

func TestListTokens(t *testing.T) {
	tests := []test{
		{
			name:       "fails to list tokens when not admin",
			want:       http.StatusUnauthorized,
			respFile:   "TestListTokens/fails_to_list_tokens_when_not_admin_response.json",
			authHeader: userAuthHeader,
			url:        "/projects/undeletableprojecttargets/tokens",
			method:     "GET",
		},
		{
			name:       "can list tokens",
			want:       http.StatusOK,
			respFile:   "TestListTokens/can_list_tokens_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/undeletableprojecttargets/tokens",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, p string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1", Repository: "repo"}, nil
				},
				ListTokenEntriesFunc: func(ctx context.Context, project string) ([]db.TokenEntry, error) {
					return []db.TokenEntry{
						{
							CreatedAt: "2022-06-21T14:56:10.341066-07:00",
							ExpiresAt: "2023-06-21T14:56:10.341066-07:00",
							TokenID:   "ghi789",
						},
						{
							CreatedAt: "2022-06-21T14:43:16.172896-07:00",
							ExpiresAt: "2023-06-21T14:43:16.172896-07:00",
							TokenID:   "def456",
						},
						{
							CreatedAt: "2022-06-21T14:42:50.182037-07:00",
							ExpiresAt: "2023-06-21T14:42:50.182037-07:00",
							TokenID:   "abc123",
						},
					}, nil
				},
			},
		},
		{
			name:       "project not found",
			want:       http.StatusNotFound,
			respFile:   "TestListTokens/project_not_found_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectdoesnotexist/tokens",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, nil },
			},
		},
		{
			name:       "no tokens",
			want:       http.StatusOK,
			respFile:   "TestListTokens/no_tokens_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectnotokens/tokens",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, p string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "abc123", Repository: "repo"}, nil
				},
				ListTokenEntriesFunc: func(ctx context.Context, project string) ([]db.TokenEntry, error) {
					return []db.TokenEntry{}, nil
				},
			},
		},
		{
			name:       "project read error",
			want:       http.StatusInternalServerError,
			respFile:   "TestListTokens/project_read_error_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectreaderror/tokens",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return false, errors.New("error retrieving project") },
			},
		},
		{
			name:       "list tokens read error",
			want:       http.StatusInternalServerError,
			respFile:   "TestListTokens/list_tokens_error_response.json",
			authHeader: adminAuthHeader,
			url:        "/projects/projectlisttokenserror/tokens",
			method:     "GET",
			cpMock: &th.CredsProviderMock{
				ProjectExistsFunc: func(s string) (bool, error) { return true, nil },
			},
			dbMock: &th.DBClientMock{
				ReadProjectEntryFunc: func(ctx context.Context, project string) (db.ProjectEntry, error) {
					return db.ProjectEntry{ProjectID: "project1"}, nil
				},
				ListTokenEntriesFunc: func(ctx context.Context, project string) ([]db.TokenEntry, error) {
					return []db.TokenEntry{}, errors.New("error from DB")
				},
			},
		},
	}
	runTests(t, tests)
}

func TestHealthCheck(t *testing.T) {
	tests := []struct {
		name                  string
		endpoint              string // Used to cause a connection error.
		vaultStatusCode       int
		writeBadContentLength bool // Used to create response body error.
		wantResponseBody      string
		wantStatusCode        int
		wantResponseHeader    string
		dbMock                *th.DBClientMock
	}{
		{
			name:               "good_vault_200",
			vaultStatusCode:    http.StatusOK,
			wantResponseBody:   "Health check succeeded\n",
			wantStatusCode:     http.StatusOK,
			wantResponseHeader: "text/plain",
			dbMock: &th.DBClientMock{
				HealthFunc: func(ctx context.Context) error {
					return nil
				},
			},
		},
		{
			name:               "good_vault_429",
			vaultStatusCode:    http.StatusTooManyRequests,
			wantResponseBody:   "Health check succeeded\n",
			wantStatusCode:     http.StatusOK,
			wantResponseHeader: "text/plain",
			dbMock: &th.DBClientMock{
				HealthFunc: func(ctx context.Context) error {
					return nil
				},
			},
		},
		{
			// We want successful health check in this vault error scenario.
			name:                  "error_vault_read_response",
			vaultStatusCode:       http.StatusOK,
			writeBadContentLength: true,
			wantResponseBody:      "Health check succeeded\n",
			wantStatusCode:        http.StatusOK,
			wantResponseHeader:    "text/plain",
			dbMock: &th.DBClientMock{
				HealthFunc: func(ctx context.Context) error {
					return nil
				},
			},
		},
		{
			name:             "error_vault_connection",
			endpoint:         string('\f'),
			wantResponseBody: "Health check failed\n",
			wantStatusCode:   http.StatusServiceUnavailable,
		},
		{
			name:             "error_vault_unhealthy_status_code",
			vaultStatusCode:  http.StatusInternalServerError,
			wantResponseBody: "Health check failed\n",
			wantStatusCode:   http.StatusServiceUnavailable,
		},
		{
			name:             "bad_db",
			vaultStatusCode:  http.StatusOK,
			wantResponseBody: "Health check failed\n",
			wantStatusCode:   http.StatusServiceUnavailable,
			dbMock: &th.DBClientMock{
				HealthFunc: func(ctx context.Context) error {
					return errors.New("too many connections")
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantURL := "/v1/sys/health"

			vaultSvc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != wantURL {
					http.NotFound(w, r)
				}

				if r.Method != http.MethodGet {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}

				if tt.writeBadContentLength {
					w.Header().Set("Content-Length", "1")
				}

				w.WriteHeader(tt.vaultStatusCode)
			}))
			defer vaultSvc.Close()

			vaultEndpoint := vaultSvc.URL
			if tt.endpoint != "" {
				vaultEndpoint = tt.endpoint
			}

			h := handler{
				logger: log.NewNopLogger(),
				env: env.Vars{
					VaultAddress: vaultEndpoint,
				},
				dbClient: tt.dbMock,
			}

			// Dummy request.
			req, err := http.NewRequest("", "", nil)
			if err != nil {
				assert.Nil(t, err)
			}

			resp := httptest.NewRecorder()

			h.healthCheck(resp, req)

			respResult := resp.Result()
			defer respResult.Body.Close()

			body, err := io.ReadAll(respResult.Body)
			assert.Nil(t, err)

			assert.Equal(t, tt.wantStatusCode, respResult.StatusCode)
			assert.Equal(t, tt.wantResponseBody, string(body))
			assert.Equal(t, tt.wantResponseHeader, respResult.Header.Get("Content-Type"))
		})
	}
}

// Serialize a type to JSON-encoded byte buffer.
func serialize(toMarshal interface{}) *bytes.Buffer {
	jsonStr, _ := json.Marshal(toMarshal)
	return bytes.NewBuffer(jsonStr)
}

func runTests(t *testing.T, tests []test) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := loadConfig(testConfigPath)
			if err != nil {
				panic(fmt.Sprintf("Unable to load config %s", err))
			}

			defaultCP := func(a credentials.Authorization, env env.Vars, h http.Header, f credentials.VaultConfigFn, fn credentials.VaultSvcFn) (credentials.Provider, error) {
				return &th.CredsProviderMock{}, nil
			}

			h := handler{
				logger:                 log.NewNopLogger(),
				newCredentialsProvider: defaultCP,
				argoCtx:                context.Background(),
				config:                 config,
				gitClient:              &th.GitClientMock{},
				env: env.Vars{
					AdminSecret: testPassword,
				},
			}

			if tt.dbMock != nil {
				h.dbClient = tt.dbMock
			}

			if tt.cpMock != nil {
				mockCP := func(a credentials.Authorization, env env.Vars, h http.Header, f credentials.VaultConfigFn, fn credentials.VaultSvcFn) (credentials.Provider, error) {
					return tt.cpMock, nil
				}

				h.newCredentialsProvider = mockCP
			}

			if tt.gitMock != nil {
				h.gitClient = tt.gitMock
			}

			if tt.wfMock != nil {
				h.argo = tt.wfMock
			}

			resp := executeRequestWithHandler(h, tt.method, tt.url, serialize(tt.req), tt.authHeader)
			if resp.StatusCode != tt.want {
				t.Errorf("Unexpected status code %d", resp.StatusCode)
			}

			if tt.body != "" {
				bodyBytes, err := io.ReadAll(resp.Body)
				defer resp.Body.Close()
				if err != nil {
					t.Errorf("Error loading body")
				}
				if tt.body != string(bodyBytes) {
					t.Errorf("Unexpected body '%s', expected '%s'", bodyBytes, tt.body)
				}
			}

			if tt.respFile != "" {
				wantBody, err := loadFileBytes(tt.respFile)
				if err != nil {
					t.Fatalf("unable to read response file '%s', err: '%s'", tt.respFile, err)
				}

				body, err := io.ReadAll(resp.Body)
				assert.Nil(t, err)

				defer resp.Body.Close()

				bodyStr := string(body)
				wantBodyStr := string(wantBody)

				// don't use assert.JSONEq for empty strings
				if bodyStr == "" && wantBodyStr == "" {
					assert.Equal(t, wantBodyStr, bodyStr)
				} else {
					assert.JSONEq(t, wantBodyStr, bodyStr)
				}
			}
		})
	}
}

func executeRequestWithHandler(h handler, method string, url string, body *bytes.Buffer, authHeader string) *http.Response {
	router := setupRouter(h)
	req, _ := http.NewRequest(method, url, body)

	req.Header.Add("Authorization", authHeader)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Result()
}

// loadFileBytes returns the contents of a file in the 'testdata' directory.
func loadFileBytes(filename string) ([]byte, error) {
	file := filepath.Join("test/testdata", filename)
	return os.ReadFile(file)
}

// loadJSON unmarshals a JSON file from the testdata directory into output.
func loadJSON(t *testing.T, filename string) (output interface{}) {
	data, err := loadFileBytes(filename)
	if err != nil {
		t.Fatalf("failed to read file %s: %v", filename, err)
	}

	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("failed to decode file %s: %v", filename, err)
	}
	return output
}
