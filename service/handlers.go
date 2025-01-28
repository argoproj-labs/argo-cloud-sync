package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cello-proj/cello/internal/requests"
	"github.com/cello-proj/cello/internal/responses"
	"github.com/cello-proj/cello/internal/types"
	"github.com/cello-proj/cello/service/internal/credentials"
	"github.com/cello-proj/cello/service/internal/db"
	"github.com/cello-proj/cello/service/internal/env"
	"github.com/cello-proj/cello/service/internal/git"
	"github.com/cello-proj/cello/service/internal/workflow"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/gorilla/mux"
	upper "github.com/upper/db/v4"
	"gopkg.in/yaml.v2"
)

const (
	numOfTokensLimit = 2
)

// Represents a JWT token.
type token struct {
	Token string `json:"token"`
}

// Represents an error response.
type errorResponse struct {
	ErrorMessage string `json:"error_message"`
}

// Generates error response JSON.
func generateErrorResponseJSON(message string) string {
	er := errorResponse{
		ErrorMessage: message,
	}
	// TODO swallowing error since this is only internally ever passed message
	jsonData, _ := json.Marshal(er)
	return string(jsonData)
}

// HTTP handler
type handler struct {
	logger                 log.Logger
	newCredentialsProvider func(a credentials.Authorization, env env.Vars, h http.Header, vaultConfig credentials.VaultConfigFn, fn credentials.VaultSvcFn) (credentials.Provider, error)
	argo                   workflow.Workflow
	argoCtx                context.Context
	config                 *Config
	gitClient              git.Client
	env                    env.Vars
	dbClient               db.Client
}

// Service HealthCheck
func (h *handler) healthCheck(w http.ResponseWriter, r *http.Request) {
	vaultEndpoint := fmt.Sprintf("%s/v1/sys/health", h.env.VaultAddress)
	l := h.requestLogger(r, "op", "health-check", "vault-endpoint", vaultEndpoint)

	// #nosec
	response, err := http.Get(vaultEndpoint)
	if err != nil {
		level.Error(l).Log("message", "received error connecting to vault", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "Health check failed")
		return
	}

	// We don't care about the body but need to read it all and close it
	// regardless.
	// https://golang.org/pkg/net/http/#Client.Do
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	if err != nil {
		level.Warn(l).Log("message", "unable to read vault body; continuing", "error", err)
		// Continue on and handle the actual response code from Vault accordingly.
	}

	if response.StatusCode != 200 && response.StatusCode != 429 {
		level.Error(l).Log("message", fmt.Sprintf("received code %d which is not 200 (initialized, unsealed, and active) or 429 (unsealed and standby) when connecting to vault", response.StatusCode))
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "Health check failed")
		return
	}

	if err = h.dbClient.Health(r.Context()); err != nil {
		level.Error(l).Log("message", fmt.Sprintf("received code error %s when connecting to database", err.Error()))
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, "Health check failed")
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "Health check succeeded")
}

// Lists workflows
func (h handler) listWorkflows(w http.ResponseWriter, r *http.Request) {
	// TODO authenticate user can list this workflow once auth figured out
	// TODO fail if project / target does not exist or are not valid format
	vars := mux.Vars(r)
	projectName := vars["projectName"]
	targetName := vars["targetName"]

	l := h.requestLogger(r, "op", "list-workflows", "project", projectName, "target", targetName)

	level.Debug(l).Log("message", "listing workflows")
	workflowList, err := h.argo.ListStatus(h.argoCtx)
	if err != nil {
		level.Error(l).Log("message", "error listing workflows", "error", err)
		h.errorResponse(w, "error listing workflows", http.StatusInternalServerError)
		return
	}

	// Only return workflows the target project / target
	workflows := make([]workflow.Status, 0)
	prefix := fmt.Sprintf("%s-%s", projectName, targetName)
	for _, wf := range workflowList {
		if strings.HasPrefix(wf.Name, prefix) {
			workflows = append(workflows, wf)
		}
	}

	jsonData, err := json.Marshal(workflows)
	if err != nil {
		level.Error(l).Log("message", "error serializing workflow IDs", "error", err)
		h.errorResponse(w, "error serializing workflow IDs", http.StatusInternalServerError)
		return
	}

	fmt.Fprintln(w, string(jsonData))
}

// Creates workflow init params by pulling manifest from given git repo, commit sha, and code path
func (h handler) loadCreateWorkflowRequestFromGit(repository, commitHash, path string) (requests.CreateWorkflow, error) {
	level.Debug(h.logger).Log("message", fmt.Sprintf("retrieving manifest from repository %s at sha %s with path %s", repository, commitHash, path))
	fileContents, err := h.gitClient.GetManifestFile(repository, commitHash, path)
	if err != nil {
		return requests.CreateWorkflow{}, err
	}

	var cwr requests.CreateWorkflow
	err = yaml.Unmarshal(fileContents, &cwr)
	return cwr, err
}

func (h handler) createWorkflowFromGit(w http.ResponseWriter, r *http.Request) {
	l := h.requestLogger(r, "op", "create-workflow-from-git")

	ctx := r.Context()

	level.Debug(l).Log("message", "validating authorization header for create workflow from git")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	// TODO we need to ensure this _isn't an admin...
	if err := a.Validate(); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "reading request body")
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		level.Error(l).Log("message", "error reading request data", "error", err)
		h.errorResponse(w, "error reading request data", http.StatusInternalServerError)
		return
	}

	var cgwr requests.CreateGitWorkflow
	err = json.Unmarshal(reqBody, &cgwr)
	if err != nil {
		level.Error(l).Log("message", "error deserializing request body", "error", err)
		h.errorResponse(w, "error deserializing request body", http.StatusBadRequest)
		return
	}

	if err := cgwr.Validate(); err != nil {
		level.Error(l).Log("message", "error validating request", "error", err)
		h.errorResponse(w, fmt.Sprintf("invalid request, %s", err), http.StatusBadRequest)
		return
	}

	vars := mux.Vars(r)
	projectName := vars["projectName"]
	projectEntry, err := h.dbClient.ReadProjectEntry(ctx, projectName)
	if err != nil {
		level.Error(l).Log("message", "error reading project data", "error", err)
		h.errorResponse(w, "error reading project data", http.StatusInternalServerError)
		return
	}

	cwr, err := h.loadCreateWorkflowRequestFromGit(projectEntry.Repository, cgwr.CommitHash, cgwr.Path)
	if err != nil {
		level.Error(l).Log("message", "error loading workflow data from git", "error", err)
		h.errorResponse(w, "error loading workflow data from git", http.StatusInternalServerError)
		return
	}

	log.With(l, "project", cwr.ProjectName, "target", cwr.TargetName, "framework", cwr.Framework, "type", cwr.Type, "workflow-template", cwr.WorkflowTemplateName)

	level.Debug(l).Log("message", "creating workflow")
	h.createWorkflowFromRequest(ctx, w, r, a, cwr, l)
}

// Creates a workflow
func (h handler) createWorkflow(w http.ResponseWriter, r *http.Request) {
	l := h.requestLogger(r, "op", "create-workflow")

	ctx := r.Context()

	level.Debug(l).Log("message", "validating authorization header for create workflow")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "reading request body")
	var cwr requests.CreateWorkflow
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		level.Error(l).Log("message", "error reading workflow request data", "error", err)
		h.errorResponse(w, "error reading workflow request data", http.StatusInternalServerError)
		return
	}

	if err := json.Unmarshal(reqBody, &cwr); err != nil {
		level.Error(l).Log("message", "error deserializing workflow data", "error", err)
		h.errorResponse(w, "error deserializing workflow data", http.StatusBadRequest)
		return
	}

	log.With(l, "project", cwr.ProjectName, "target", cwr.TargetName, "framework", cwr.Framework, "type", cwr.Type, "workflow-template", cwr.WorkflowTemplateName)
	level.Debug(l).Log("message", "creating workflow")
	h.createWorkflowFromRequest(ctx, w, r, a, cwr, l)
}

// Creates a workflow
// Context is not currently used as Argo has its own and Vault doesn't
// currently support it.
func (h handler) createWorkflowFromRequest(_ context.Context, w http.ResponseWriter, r *http.Request, a *credentials.Authorization, cwr requests.CreateWorkflow, l log.Logger) {
	types, err := h.config.listTypes(cwr.Framework)
	if err != nil {
		level.Error(l).Log("message", "error invalid framework", "error", err)
		h.errorResponse(
			w,
			fmt.Sprintf("invalid request, framework must be one of '%s'", strings.Join(h.config.listFrameworks(), " ")),
			http.StatusBadRequest,
		)
		return
	}

	level.Debug(l).Log("message", "validating workflow parameters")
	if err := cwr.Validate(
		cwr.ValidateType(types),
	); err != nil {
		level.Error(l).Log("message", "error validating request", "error", err)
		h.errorResponse(w, fmt.Sprintf("error invalid request, %s", err), http.StatusBadRequest)
		return
	}

	workflowFrom := fmt.Sprintf("workflowtemplate/%s", cwr.WorkflowTemplateName)
	executeContainerImageURI := cwr.Parameters["execute_container_image_uri"]
	environmentVariablesString := generateEnvVariablesString(cwr.EnvironmentVariables)

	level.Debug(l).Log("message", "generating command to execute")
	commandDefinition, err := h.config.getCommandDefinition(cwr.Framework, cwr.Type)
	if err != nil {
		level.Error(l).Log("message", "unable to get command definition", "error", err)
		h.errorResponse(w, "unable to retrieve command definition", http.StatusInternalServerError)
		return
	}
	executeCommand, err := generateExecuteCommand(commandDefinition, environmentVariablesString, cwr.Arguments)
	if err != nil {
		level.Error(l).Log("message", "unable to generate command", "error", err)
		h.errorResponse(w, "unable to generate command", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "creating new credentials provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "bad or unknown credentials provider", "error", err)
		h.errorResponse(w, "bad or unknown credentials provider", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "getting credentials provider token")
	credentialsToken, err := cp.GetToken()
	if err != nil {
		level.Error(l).Log("message", "error getting credentials provider token", "error", err)
		h.errorResponse(w, "error retrieving credentials provider token", http.StatusInternalServerError)
		return
	}

	projectExists, err := cp.ProjectExists(cwr.ProjectName)
	if err != nil {
		level.Error(l).Log("message", "error checking project", "error", err)
		h.errorResponse(w, "error checking project", http.StatusInternalServerError)
		return
	}

	if !projectExists {
		level.Error(l).Log("message", "project does not exist", "error", err)
		h.errorResponse(w, "project does not exist", http.StatusBadRequest)
		return
	}

	targetExists, err := cp.TargetExists(cwr.ProjectName, cwr.TargetName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving target", "error", err)
		h.errorResponse(w, "error retrieving target", http.StatusInternalServerError)
		return
	}
	if !targetExists {
		level.Error(l).Log("message", "target not found")
		h.errorResponse(w, "target not found", http.StatusBadRequest)
		return
	}

	level.Debug(l).Log("message", "creating workflow parameters")
	parameters := workflow.NewParameters(environmentVariablesString, executeCommand, executeContainerImageURI, cwr.TargetName, cwr.ProjectName, cwr.Parameters, credentialsToken, cwr.Type)

	workflowLabels := map[string]string{txIDHeader: r.Header.Get(txIDHeader)}

	level.Debug(l).Log("message", "creating workflow")
	workflowName, err := h.argo.Submit(h.argoCtx, workflowFrom, parameters, workflowLabels)
	if err != nil {
		level.Error(l).Log("message", "error creating workflow", "error", err)
		h.errorResponse(w, "error creating workflow", http.StatusInternalServerError)
		return
	}

	l = log.With(l, "workflow", workflowName)
	level.Debug(l).Log("message", "workflow created")
	tokenHead := credentialsToken[0:8]

	level.Info(l).Log("message", fmt.Sprintf("Received token '%s...'", tokenHead))
	var cwresp workflow.CreateWorkflowResponse
	cwresp.WorkflowName = workflowName
	jsonData, err := json.Marshal(cwresp)
	if err != nil {
		level.Error(l).Log("message", "error serializing workflow response", "error", err)
		h.errorResponse(w, "error serializing workflow response", http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, string(jsonData))
}

// Gets a workflow
func (h handler) getWorkflow(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	workflowName := vars["workflowName"]
	l := h.requestLogger(r, "op", "get-workflow", "workflow", workflowName)

	level.Debug(l).Log("message", "getting workflow status")
	status, err := h.argo.Status(h.argoCtx, workflowName)

	if err != nil {
		if strings.Contains(err.Error(), "code = NotFound") {
			level.Error(l).Log("message", "error getting workflow", "error", err)
			h.errorResponse(w, "workflow not found", http.StatusNotFound)
		} else {
			level.Error(l).Log("message", "error getting workflow", "error", err)
			h.errorResponse(w, "error getting workflow", http.StatusInternalServerError)
		}

		return
	}

	level.Debug(l).Log("message", "decoding get workflow response")
	jsonData, err := json.Marshal(status)
	if err != nil {
		level.Error(l).Log("message", "error serializing workflow", "error", err)
		h.errorResponse(w, "error serializing workflow", http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(jsonData))
}

// Gets a target
func (h handler) getTarget(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]
	targetName := vars["targetName"]

	l := h.requestLogger(r, "op", "get-target", "project", projectName, "target", targetName)

	level.Debug(l).Log("message", "validating authorization header for get target")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	targetExists, err := cp.TargetExists(projectName, targetName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving target", "error", err)
		h.errorResponse(w, "error retrieving target", http.StatusInternalServerError)
		return
	}

	if !targetExists {
		level.Error(l).Log("message", "target not found")
		h.errorResponse(w, "target not found", http.StatusNotFound)
		return
	}

	level.Debug(l).Log("message", "getting target information")
	targetInfo, err := cp.GetTarget(projectName, targetName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving target information", "error", err)
		h.errorResponse(w, "error retrieving target information", http.StatusInternalServerError)
		return
	}

	jsonResult, err := json.Marshal(targetInfo)
	if err != nil {
		level.Error(l).Log("message", "error serializing json target data", "error", err)
		h.errorResponse(w, "error serializing json target data", http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(jsonResult))
}

// Returns the logs for a workflow
func (h handler) getWorkflowLogs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	workflowName := vars["workflowName"]

	l := h.requestLogger(r, "op", "get-workflow-logs", "workflow", workflowName)

	level.Debug(l).Log("message", "retrieving workflow logs")
	argoWorkflowLogs, err := h.argo.Logs(h.argoCtx, workflowName)
	if err != nil {
		level.Error(l).Log("message", "error getting workflow logs", "error", err)
		h.errorResponse(w, "error getting workflow logs", http.StatusInternalServerError)
		return
	}

	jsonData, err := json.Marshal(argoWorkflowLogs)
	if err != nil {
		level.Error(l).Log("message", "error serializing workflow logs", "error", err)
		h.errorResponse(w, "error serializing workflow logs", http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, string(jsonData))
}

// Streams workflow logs
func (h handler) getWorkflowLogStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-Accel-Buffering", "no")
	vars := mux.Vars(r)
	workflowName := vars["workflowName"]

	l := h.requestLogger(r, "op", "get-workflow-log-stream", "workflow", workflowName)

	level.Debug(l).Log("message", "retrieving workflow logs", "workflow", workflowName)
	err := h.argo.LogStream(h.argoCtx, workflowName, w)
	if err != nil {
		level.Error(l).Log("message", "error getting workflow logstream", "error", err)
		h.errorResponse(w, "error getting workflow logs", http.StatusInternalServerError)
		return
	}
}

// Returns a new Cello token
func newCelloToken(provider string, tok types.Token) *token {
	return &token{
		Token: fmt.Sprintf("%s:%s:%s", provider, tok.RoleID, tok.Secret),
	}
}

// projectExists checks if a project exists using both the credential provider and database
func (h handler) projectExists(ctx context.Context, l log.Logger, cp credentials.Provider, w http.ResponseWriter, projectName string) (bool, error) {
	// Checking credential provider
	level.Debug(l).Log("message", "checking if project exists")
	projectExists, err := cp.ProjectExists(projectName)
	if err != nil {
		level.Error(l).Log("message", "error checking credentials provider for project", "error", err)
		h.errorResponse(w, "error retrieving project", http.StatusInternalServerError)
		return false, err
	}

	if !projectExists {
		level.Debug(l).Log("message", "project does not exist in credentials provider")
		h.errorResponse(w, "project does not exist", http.StatusNotFound)
		return false, err
	}

	// Checking database
	_, err = h.dbClient.ReadProjectEntry(ctx, projectName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving project from database", "error", err)
		if errors.Is(err, upper.ErrNoMoreRows) {
			h.errorResponse(w, "project does not exist", http.StatusNotFound)
		} else {
			h.errorResponse(w, "error retrieving project", http.StatusInternalServerError)
		}
		return false, err
	}

	return true, err
}

// Creates a project
func (h handler) createProject(w http.ResponseWriter, r *http.Request) {
	l := h.requestLogger(r, "op", "create-project")

	level.Debug(l).Log("message", "validating authorization header for create project")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	var capp requests.CreateProject
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		level.Error(l).Log("message", "error reading request body", "error", err)
		h.errorResponse(w, "error reading request body", http.StatusInternalServerError)
		return
	}
	if err := json.Unmarshal(reqBody, &capp); err != nil {
		level.Error(l).Log("message", "error decoding request", "error", err)
		h.errorResponse(w, "error decoding request", http.StatusBadRequest)
		return
	}
	if err := capp.Validate(); err != nil {
		level.Error(l).Log("message", "error invalid request", "error", err)
		h.errorResponse(w, fmt.Sprintf("invalid request, %s", err.Error()), http.StatusBadRequest)
		return
	}

	l = log.With(l, "project", capp.Name)

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	projectExists, err := cp.ProjectExists(capp.Name)
	if err != nil {
		level.Error(l).Log("message", "error checking project", "error", err)
		h.errorResponse(w, "error checking project", http.StatusInternalServerError)
		return
	}

	if projectExists {
		level.Error(l).Log("error", "project already exists")
		h.errorResponse(w, "project already exists", http.StatusBadRequest)
		return
	}

	level.Debug(l).Log("message", "inserting into db")
	err = h.dbClient.CreateProjectEntry(ctx, db.ProjectEntry{
		ProjectID:  capp.Name,
		Repository: capp.Repository,
	})
	if err != nil {
		level.Error(l).Log("message", "error inserting project to db", "error", err)
		h.errorResponse(w, "error creating project", http.StatusInternalServerError)
		return
	}
	level.Debug(l).Log("message", "creating project")
	token, err := cp.CreateProject(capp.Name)
	if err != nil {
		level.Error(l).Log("message", "error creating project", "error", err)
		h.errorResponse(w, "error creating project", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "inserting token into DB")
	err = h.dbClient.CreateTokenEntry(ctx, token)
	if err != nil {
		level.Error(l).Log("message", "error inserting token into DB", "error", err)
		h.errorResponse(w, "error creating token", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "retrieving Cello token")
	celloToken := newCelloToken("vault", token)

	resp := responses.CreateProject{
		Token:   celloToken.Token,
		TokenID: token.ProjectToken.ID,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		level.Error(l).Log("message", "error serializing token", "error", err)
		h.errorResponse(w, "error serializing token", http.StatusInternalServerError)
		return
	}
}

// Get a project
func (h handler) getProject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]

	l := h.requestLogger(r, "op", "get-project", "project", projectName)

	level.Debug(l).Log("message", "validating authorization header for get project")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "getting project from database")
	ctx := r.Context()
	projectEntry, err := h.dbClient.ReadProjectEntry(ctx, projectName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving project", "error", err)
		if errors.Is(err, upper.ErrNoMoreRows) {
			h.errorResponse(w, "error retrieving project", http.StatusNotFound)
		} else {
			h.errorResponse(w, "error retrieving project", http.StatusInternalServerError)
		}
		return
	}

	resp := responses.GetProject{
		Name:       projectName,
		Repository: projectEntry.Repository,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		level.Error(l).Log("message", "error creating response", "error", err)
		h.errorResponse(w, "error creating response object", http.StatusInternalServerError)
		return
	}
}

// Delete a project
func (h handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]

	l := h.requestLogger(r, "op", "delete-project", "project", projectName)

	level.Debug(l).Log("message", "validating authorization header for delete project")
	ctx := r.Context()

	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "checking if project exists")
	projectExists, err := cp.ProjectExists(projectName)
	if err != nil {
		level.Error(l).Log("message", "error checking project", "error", err)
		h.errorResponse(w, "error checking project", http.StatusInternalServerError)
		return
	}

	if !projectExists {
		level.Debug(l).Log("message", "no action required because project does not exist")
		return
	}

	level.Debug(l).Log("message", "getting all targets in project")
	targets, err := cp.ListTargets(projectName)
	if err != nil {
		level.Error(l).Log("message", "error getting all targets", "error", err)
		h.errorResponse(w, "error getting all targets", http.StatusInternalServerError)
		return
	}

	if len(targets) > 0 {
		level.Error(l).Log("error", "project has existing targets, not deleting")
		h.errorResponse(w, "project has existing targets, not deleting", http.StatusBadRequest)
		return
	}

	level.Debug(l).Log("message", "deleting project")
	err = cp.DeleteProject(projectName)
	if err != nil {
		level.Error(l).Log("message", "error deleting project", "error", err)
		h.errorResponse(w, "error deleting project", http.StatusInternalServerError)
		return
	}

	level.Debug(h.logger).Log("message", "deleting from db")
	if err = h.dbClient.DeleteProjectEntry(ctx, projectName); err != nil {
		level.Error(l).Log("message", "error deleting project in database", "error", err)
		h.errorResponse(w, "error deleting project", http.StatusInternalServerError)
		return
	}
}

// Creates a target
func (h handler) createTarget(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]

	l := h.requestLogger(r, "op", "create-target", "project", projectName)

	level.Debug(l).Log("message", "validating authorization header for create target")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	level.Debug(l).Log("message", "reading request body")

	var ctr requests.CreateTarget
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		level.Error(l).Log("message", "error reading request data", "error", err)
		h.errorResponse(w, "error reading request data", http.StatusInternalServerError)
	}

	if err := json.Unmarshal(reqBody, &ctr); err != nil {
		level.Error(l).Log("message", "error processing request", "error", err)
		h.errorResponse(w, "error processing request", http.StatusBadRequest)
		return
	}

	if err := types.Target(ctr).Validate(); err != nil {
		level.Error(l).Log("message", "error invalid request", "error", err)
		h.errorResponse(w, fmt.Sprintf("invalid request, %s", err), http.StatusBadRequest)
		return
	}

	l = log.With(l, "target", ctr.Name)

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	projectExists, err := cp.ProjectExists(projectName)
	if err != nil {
		level.Error(l).Log("message", "error determining if project exists", "error", err)
	}

	// TODO Perhaps this should be 404
	if !projectExists {
		level.Error(l).Log("message", "project does not exist")
		h.errorResponse(w, "project does not exist", http.StatusBadRequest)
		return
	}

	targetExists, err := cp.TargetExists(projectName, ctr.Name)
	if err != nil {
		level.Error(l).Log("message", "error retrieving target", "error", err)
		h.errorResponse(w, "error retrieving target", http.StatusInternalServerError)
		return
	}
	if targetExists {
		level.Error(l).Log("message", "target name must not already exist")
		h.errorResponse(w, "target name must not already exist", http.StatusBadRequest)
		return
	}

	level.Debug(l).Log("message", "creating target")
	err = cp.CreateTarget(projectName, types.Target(ctr))
	if err != nil {
		level.Error(l).Log("message", "error creating target", "error", err)
		h.errorResponse(w, "error creating target", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "{}")
}

// Deletes a target
func (h handler) deleteTarget(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]
	targetName := vars["targetName"]

	l := h.requestLogger(r, "op", "delete-target", "project", projectName, "target", targetName)

	level.Debug(l).Log("message", "validating authorization header for delete target")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	targetExists, err := cp.TargetExists(projectName, targetName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving target", "error", err)
		h.errorResponse(w, "error retrieving target", http.StatusInternalServerError)
		return
	}

	if !targetExists {
		level.Error(l).Log("message", "target does not exist")
		h.errorResponse(w, "target does not exist", http.StatusNotFound)
		return
	}

	level.Debug(l).Log("message", "deleting target")
	err = cp.DeleteTarget(projectName, targetName)
	if err != nil {
		level.Error(l).Log("message", "error deleting target", "error", err)
		h.errorResponse(w, "error deleting target", http.StatusInternalServerError)
		return
	}
}

// Lists the targets for a project
func (h handler) listTargets(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]

	l := h.requestLogger(r, "op", "list-targets", "project", projectName)

	level.Debug(l).Log("message", "validating authorization header for target list")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "checking if project exists")
	projectExists, err := cp.ProjectExists(projectName)
	if err != nil {
		level.Error(l).Log("message", "error checking project", "error", err)
		h.errorResponse(w, "error checking project", http.StatusInternalServerError)
		return
	}

	if !projectExists {
		level.Debug(l).Log("message", "project does not exist")
		h.errorResponse(w, "project does not exist", http.StatusNotFound)
		return
	}

	targets, err := cp.ListTargets(projectName)
	if err != nil {
		level.Error(l).Log("message", "error listing targets", "error", err)
		h.errorResponse(w, "error listing targets", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(targets)
	if err != nil {
		level.Error(l).Log("message", "error serializing targets", "error", err)
		h.errorResponse(w, "error listing targets", http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(data))
}

// Updates a target
func (h handler) updateTarget(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]
	targetName := vars["targetName"]

	l := h.requestLogger(r, "op", "update-target", "project", projectName, "target", targetName)

	level.Debug(l).Log("message", "validating authorization header for update target")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	projectExists, err := cp.ProjectExists(projectName)
	if err != nil {
		level.Error(l).Log("message", "error determining if project exists", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	if !projectExists {
		level.Error(l).Log("message", "project does not exist")
		h.errorResponse(w, "project does not exist", http.StatusNotFound)
		return
	}

	targetExists, err := cp.TargetExists(projectName, targetName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving target", "error", err)
		h.errorResponse(w, "error retrieving target", http.StatusInternalServerError)
		return
	}
	if !targetExists {
		level.Error(l).Log("message", "target not found")
		h.errorResponse(w, "target not found", http.StatusNotFound)
		return
	}

	target, err := cp.GetTarget(projectName, targetName)
	if err != nil {
		level.Error(l).Log("message", "error retrieving existing target")
		h.errorResponse(w, "error retrieving target", http.StatusInternalServerError)
		return
	}
	targetType := target.Type

	level.Debug(l).Log("message", "reading request body")
	reqBody, err := io.ReadAll(r.Body)
	if err != nil {
		level.Error(l).Log("message", "error reading request data", "error", err)
		h.errorResponse(w, "error reading request data", http.StatusInternalServerError)
		return
	}

	// merge request data into existing target struct for update data
	if err := json.Unmarshal(reqBody, &target); err != nil {
		level.Error(l).Log("message", "error reading target properties data", "error", err)
		h.errorResponse(w, "error reading target properties data", http.StatusInternalServerError)
		return
	}
	// overwrite updated target with existing target name and type values so request body doesn't overwrite these values
	target.Name = targetName
	target.Type = targetType

	if err := target.Validate(); err != nil {
		level.Error(l).Log("message", "error invalid request", "error", err)
		h.errorResponse(w, fmt.Sprintf("invalid request, %s", err), http.StatusBadRequest)
		return
	}

	level.Debug(l).Log("message", "updating target")
	err = cp.UpdateTarget(projectName, target)
	if err != nil {
		level.Error(l).Log("message", "error updating target", "error", err)
		h.errorResponse(w, "error updating target", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(target)
	if err != nil {
		level.Error(l).Log("message", "error creating response", "error", err)
		h.errorResponse(w, "error creating response object", http.StatusInternalServerError)
		return
	}

	fmt.Fprint(w, string(data))
}

func (h handler) deleteToken(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]
	tokenID := vars["tokenID"]

	l := h.requestLogger(r, "op", "delete-token", "project", projectName, "tokenID", tokenID)

	level.Debug(l).Log("message", "validating authorization header for delete token")

	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	level.Debug(l).Log("message", "checking if project exists")
	projectExists, err := h.projectExists(ctx, l, cp, w, projectName)

	if err != nil || !projectExists {
		return
	}

	// check if token exists in CP and DB
	projectToken, err := cp.GetProjectToken(projectName, tokenID)
	if err != nil {
		// do not return an error if project token is not found
		if !errors.Is(err, credentials.ErrProjectTokenNotFound) {
			level.Error(l).Log("message", "error retrieving token from credentials provider", "error", err)
			h.errorResponse(w, "error retrieving token", http.StatusInternalServerError)
			return
		}
		level.Warn(l).Log("message", "token does not exist in credential provider", "error", err)
	}

	dbProjectToken, err := h.dbClient.ReadTokenEntry(ctx, tokenID)
	if err != nil {
		// do not return an error if project token is not found
		if !errors.Is(err, upper.ErrNoMoreRows) {
			level.Error(l).Log("message", "error retrieving token from DB", "error", err)
			h.errorResponse(w, "error retrieving token", http.StatusInternalServerError)
			return
		}
		level.Warn(l).Log("message", "token does not exist in DB", "error", err)
	}

	// delete token from DB and CP
	// only delete token if exists in DB
	if !dbProjectToken.IsEmpty() {
		level.Debug(l).Log("message", "deleting token from database")
		if err = h.dbClient.DeleteTokenEntry(ctx, tokenID); err != nil {
			level.Error(l).Log("message", "error deleting token from database", "error", err)
			h.errorResponse(w, "error deleting token", http.StatusInternalServerError)
			return
		}
	}

	// only delete token if exists in CP
	if !projectToken.IsEmpty() {
		level.Debug(l).Log("message", "deleting token from credentials provider")
		if err = cp.DeleteProjectToken(projectName, tokenID); err != nil {
			level.Error(l).Log("message", "error deleting token from credentials provider", "error", err)
			h.errorResponse(w, "error deleting token", http.StatusInternalServerError)
			return
		}
	}
}

// Creates a token
func (h handler) createToken(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]

	l := h.requestLogger(r, "op", "create-token", "project", projectName)

	level.Debug(l).Log("message", "validating authorization header for token create")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}
	projectExists, err := h.projectExists(ctx, l, cp, w, projectName)

	if err != nil || !projectExists {
		return
	}

	tokens, err := h.dbClient.ListTokenEntries(ctx, projectName)
	if err != nil {
		level.Error(l).Log("message", "error listing tokens from DB", "error", err)
		h.errorResponse(w, "error listing tokens", http.StatusInternalServerError)
		return
	}

	if len(tokens) >= numOfTokensLimit {
		level.Error(l).Log("message", "number of tokens allowed per project has been reached")
		h.errorResponse(w, "token limit reached", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "creating token")
	token, err := cp.CreateToken(projectName)
	if err != nil {
		level.Error(l).Log("message", "error creating token with credentials provider", "error", err)
		h.errorResponse(w, "error creating token with credentials provider", http.StatusInternalServerError)
		return
	}

	level.Debug(l).Log("message", "inserting into db")
	err = h.dbClient.CreateTokenEntry(ctx, token)
	if err != nil {
		level.Error(l).Log("message", "error inserting token to db", "error", err)
		h.errorResponse(w, "error creating token", http.StatusInternalServerError)
		return
	}

	celloToken := newCelloToken("vault", token)

	resp := responses.CreateToken{
		CreatedAt: token.CreatedAt,
		ExpiresAt: token.ExpiresAt,
		Token:     celloToken.Token,
		TokenID:   token.ProjectToken.ID,
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		level.Error(l).Log("message", "error serializing project token", "error", err)
		h.errorResponse(w, "error listing project tokens", http.StatusInternalServerError)
		return
	}
}

// Lists tokens for a project
func (h handler) listTokens(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectName := vars["projectName"]

	l := h.requestLogger(r, "op", "list-tokens", "project", projectName)

	level.Debug(l).Log("message", "validating authorization header for token list")
	ah := r.Header.Get("Authorization")
	a, err := credentials.NewAuthorization(ah)
	if err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header format", http.StatusUnauthorized)
		return
	}
	if err := a.Validate(a.ValidateAuthorizedAdmin(h.env.AdminSecret)); err != nil {
		h.errorResponse(w, "error unauthorized, invalid authorization header", http.StatusUnauthorized)
		return
	}

	ctx := r.Context()

	level.Debug(l).Log("message", "creating credential provider")
	cp, err := h.newCredentialsProvider(*a, h.env, r.Header, credentials.NewVaultConfig, credentials.NewVaultSvc)
	if err != nil {
		level.Error(l).Log("message", "error creating credentials provider", "error", err)
		h.errorResponse(w, "error creating credentials provider", http.StatusInternalServerError)
		return
	}

	projectExists, err := h.projectExists(ctx, l, cp, w, projectName)

	if err != nil || !projectExists {
		return
	}

	tokens, err := h.dbClient.ListTokenEntries(ctx, projectName)
	if err != nil {
		level.Error(l).Log("message", "error listing project tokens", "error", err)
		h.errorResponse(w, "error listing project tokens", http.StatusInternalServerError)
		return
	}

	resp := []responses.ListTokens{}
	for _, tokenEntry := range tokens {
		resp = append(resp, responses.ListTokens{
			CreatedAt: tokenEntry.CreatedAt,
			ExpiresAt: tokenEntry.ExpiresAt,
			TokenID:   tokenEntry.TokenID,
		})
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		level.Error(l).Log("message", "error serializing project tokens", "error", err)
		h.errorResponse(w, "error listing project tokens", http.StatusInternalServerError)
		return
	}
}

// Convenience method that writes a failure response in a standard manner
func (h handler) errorResponse(w http.ResponseWriter, message string, httpStatus int) {
	r := generateErrorResponseJSON(message)
	w.WriteHeader(httpStatus)
	fmt.Fprint(w, r)
}

func generateEnvVariablesString(environmentVariables map[string]string) string {
	if len(environmentVariables) == 0 {
		return ""
	}

	r := "env"
	for k, v := range environmentVariables {
		// strip all single and double quotes in environment variables
		value := strings.ReplaceAll(strings.ReplaceAll(v, "\"", ""), "'", "")
		r = r + fmt.Sprintf(" %s='%s'", k, value)
	}
	return r
}

func (h handler) requestLogger(r *http.Request, fields ...interface{}) log.Logger {
	return log.With(
		h.logger,
		append([]interface{}{"txid", r.Header.Get(txIDHeader)}, fields...)...,
	)
}
