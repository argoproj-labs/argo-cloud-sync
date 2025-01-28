#!/bin/bash

ARGO_WORKFLOWS_VERSION=3.0.10

set -e

# check if executable exists, if not give command for installing.
# example:
#  executable_check "application" "application install command"

function executable_check()  {
  if ! command -v $1 &> /dev/null
  then
      echo "executable $1 could not be found"
      echo "to install $1 run:"
      echo "  $2"
      exit
  fi
}

# use this function by a trap on SIGINT (CTRL+C) to kill all background processes
function kill_jobs {
  jobs -p | xargs -i  sh -c 'kill {} 2>/dev/null'
}

executable_check "brew" '/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
executable_check "docker" 'brew cask install docker'
executable_check "kubectl" 'curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/darwin/amd64/kubectl"'
executable_check "argo" 'brew install argo'
executable_check "aws" 'brew install awscli'

# exit if aws credentials are not set
set +e
aws sts get-caller-identity &> /dev/null
if [ $? != 0 ]; then
  echo "aws credentials not set"
  exit 1
fi
set -e

if [ -z $CELLO_ADMIN_SECRET ]; then
  echo "CELLO_ADMIN_SECRET environment variable must be set"
  exit 1
fi

if [ ! -d scripts ]; then
  echo "this script must be run from the root of the repo"
  exit 1
fi

# create iam role if it doesn't already exist
set +e
aws cloudformation describe-stacks --stack-name CelloSampleRole --region us-west-2 &> /dev/null
if [ $? != 0 ]; then
  echo "creating iam role"
  bash scripts/create_iam_role.sh
fi
set -e

# Download artifacts
mkdir -p quickstart

latest_release=$(curl --silent "https://api.github.com/repos/cello-proj/cello/releases/latest" | jq -r .tag_name
)
# remove leading 'v'
latest_release="${latest_release//v}"

# download Cello CLI if it doesn't exist
if [ ! -f quickstart/cello ]; then
    curl -L https://github.com/cello-proj/cello/releases/download/v${latest_release}/cello_cli_${latest_release}_darwin_x86_64.tar.gz -o quickstart/cello_cli_${latest_release}_darwin_x86_64.tar.gz &> /dev/null
      tar -xzf quickstart/cello_cli_${latest_release}_darwin_x86_64.tar.gz -C quickstart/ #&> /dev/null
        rm quickstart/cello_cli_${latest_release}_darwin_x86_64.tar.gz &> /dev/null
fi

# download Cello service binary if it doesn't exist
if [ ! -f quickstart/service ]; then
  curl -L https://github.com/cello-proj/cello/releases/download/v${latest_release}/cello_service_${latest_release}_linux_x86_64.tar.gz -o quickstart/cello_service_${latest_release}_linux_x86_64.tar.gz &> /dev/null
  tar -xzf quickstart/cello_service_${latest_release}_linux_x86_64.tar.gz -C quickstart/ &> /dev/null
  rm quickstart/cello_service_${latest_release}_linux_x86_64.tar.gz &> /dev/null
fi

set +e
echo "Building docker image"
docker build --pull --rm -f "Dockerfile" --build-arg BINARY=quickstart/service -t cello:latest "."
docker build --pull --rm -f "Dockerfile.db_migration" -t cello-db-migration:latest "."
echo "Checking for Argo Workflows"
kubectl get ns | grep argo
if [ $? != 0 ]; then
  echo "Applying Argo Workflows manifest"
  kubectl create ns argo
  kubectl apply -n argo -f https://github.com/argoproj/argo-workflows/releases/download/v3.3.1/quick-start-minimal.yaml
else 
  echo "Argo Workflows found"
fi
echo "Applying manifest"
kubectl apply -f ./scripts/quickstart_manifest.yaml
# Sleeping after applying manifest so pods have time to start
while [ "$(kubectl get pods -l=app.kubernetes.io/name='vault' -o jsonpath='{.items[*].status.containerStatuses[0].ready}')" != "true" ]; do
   sleep 5
   echo "Waiting for Vault to be ready."
done
while [ "$(kubectl get pods -l=app='postgres' -o jsonpath='{.items[*].status.containerStatuses[0].ready}')" != "true" ]; do
   sleep 5
   echo "Waiting for Postgres to be ready."
done
echo "Pods ready. Initializing environment"
set -e

# setup postgres db
# don't fail if already exists
set +e
export POSTGRES_POD="$(kubectl get pods --no-headers -o custom-columns=":metadata.name" | grep postgres)"

RETRIES=20
until kubectl exec $POSTGRES_POD -- psql -d postgres -c "select 1" > /dev/null 2>&1 || [ $RETRIES -eq 0 ]; do
  echo "Waiting for postgres server to fully start up, $((RETRIES--)) remaining attempts..."
  sleep 3
done

# Check if db exists, create if not
kubectl exec $POSTGRES_POD -- psql -lqt | cut -d \| -f 1 | grep cello
if [ $? != 0 ]; then
  kubectl exec $POSTGRES_POD -- createdb cello -U postgres
fi
set -e

# run DB migration job after Postgres DB is fully stood up
echo "Applying DB migration manifest"
kubectl apply -f ./scripts/quickstart_db_migration_manifest.yaml

while : ; do
  echo "Waiting for DB migration job to complete"
  status=$(kubectl get job migrate-db -o jsonpath='{.status.conditions[].type}')
  if [ -z $status ]; then
    sleep 5
    continue
  fi
  if [ $status == 'Complete' ]; then
    break
  fi
  if [ $status == 'Failed' ]; then
    echo "ERROR: migration job failed. Please check pod logs and ."
    exit 1
  fi
done

while [ "$(kubectl get pods -l=app='cello' -o jsonpath='{.items[*].status.containerStatuses[0].ready}')" != "true" ]; do
   sleep 5
   echo "Waiting for Cello to be ready."
done

# setup workflow if it doesn't exist
set +e
argo template get -n argo cello-single-step-vault-aws &> /dev/null
if [ $? != 0 ]; then
  argo template create -n argo workflows/cello-single-step-vault-aws.yaml
fi

# setup aws credentials in vault
if [ -n "${AWS_PROFILE}" ]; then
  CREDS_PROCESS_VALUE=`aws configure get $AWS_PROFILE.credential_process`
  if [ -n "$CREDS_PROCESS_VALUE" ]; then
    # profile is using credential_process
    # we require jq make sure it exits
    if ! command -v jq >/dev/null 2>&1; then
      echo "ERROR: 'jq' command could not be found"
      exit 1
    fi

    # run credential_process - parse json from it
    CREDS_JSON=`$CREDS_PROCESS_VALUE`

    export AWS_ACCESS_KEY_ID=`echo "$CREDS_JSON" | jq -r ."AccessKeyId"`
    export AWS_SECRET_ACCESS_KEY=`echo "$CREDS_JSON" | jq -r ."SecretAccessKey"`
    export AWS_SESSION_TOKEN=`echo "$CREDS_JSON" | jq -r ."SessionToken"`
  else
    # profile isn't using credential_process, get values from profile config
    export AWS_ACCESS_KEY_ID=`aws configure get $AWS_PROFILE.aws_access_key_id`
    export AWS_SECRET_ACCESS_KEY=`aws configure get $AWS_PROFILE.aws_secret_access_key`
    export AWS_SESSION_TOKEN=`aws configure get $AWS_PROFILE.aws_session_token`
  fi
fi

if [ -z "$AWS_ACCESS_KEY_ID" ]; then
    echo "Error: AWS_ACCESS_KEY_ID was not set and could not be loaded from AWS_PROFILE"
    exit 1
fi

if [ -z "$AWS_SECRET_ACCESS_KEY" ]; then
    echo "Error: AWS_SECRET_ACCESS_KEY was not set and could not be loaded from AWS_PROFILE"
    exit 1
fi

if [ -z "$AWS_SESSION_TOKEN" ]; then
    echo "Error: AWS_SESSION_TOKEN was not set and could not be loaded from AWS_PROFILE"
    exit 1
fi

set -e

cat > /tmp/awsConfig << EOF
[default]
aws_access_key_id=$AWS_ACCESS_KEY_ID
aws_secret_access_key=$AWS_SECRET_ACCESS_KEY
aws_session_token=$AWS_SESSION_TOKEN
EOF
kubectl exec vault-0 -- mkdir -p /home/vault/.aws
kubectl cp /tmp/awsConfig vault-0:/home/vault/.aws/credentials

# Trap to wait until user hits Ctrl-C, then kill child processes.
trap kill_jobs SIGINT

# Argo Workflows UI
echo "Exposing Argo UI & API on http://localhost:2746/"
argo server --secure=false --auth-mode=server >/dev/null 2>&1 &

CELLO_LOCAL_PORT=8443
echo "Cello started, forwarding to port $CELLO_LOCAL_PORT"
kubectl port-forward service/cello-service $CELLO_LOCAL_PORT:8443 &
wait
