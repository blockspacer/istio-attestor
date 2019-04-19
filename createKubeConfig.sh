#!/bin/bash
set -e
set -o pipefail

# Add user to k8s using service account, no RBAC (must create RBAC after this script)
if [[ -z "$1" ]] || [[ -z "$2" ]]; then
 echo "usage: $0 <service_account_name> <namespace>"
 exit 1
fi

SERVICE_ACCOUNT_NAME=$1
NAMESPACE="$2"
KUBECFG_FILE_NAME="k8s-${SERVICE_ACCOUNT_NAME}-${NAMESPACE}-conf"

# Create service account if not exist
if out=$(kubectl get sa "${SERVICE_ACCOUNT_NAME}" --namespace "${NAMESPACE}"  -o jsonpath={.metadata..name} 2>/dev/null); then
    echo "Service account ${SERVICE_ACCOUNT_NAME} exist"
else
    echo "Creating a service account in ${NAMESPACE} namespace: ${SERVICE_ACCOUNT_NAME}"
    kubectl create serviceaccount "${SERVICE_ACCOUNT_NAME}" --namespace "${NAMESPACE}"
fi

echo -e "\\nGetting secret of service account ${SERVICE_ACCOUNT_NAME} on ${NAMESPACE}"
SECRET_NAME=$(kubectl get sa "${SERVICE_ACCOUNT_NAME}" --namespace="${NAMESPACE}" -o jsonpath={.secrets..name})

echo -e -n "\\nExtracting ca.crt from secret..."
kubectl get secret --namespace "${NAMESPACE}" "${SECRET_NAME}" -o "jsonpath={.data['ca\.crt']}" | base64 --decode > "sa_ca.crt"

echo -e -n "\\nGetting user token from secret..."
USER_TOKEN=$(kubectl get secret --namespace "${NAMESPACE}" "${SECRET_NAME}" -o "jsonpath={.data..token}" | base64 --decode)

context=$(kubectl config current-context)
echo -e "\\nSetting current context to: $context"

CLUSTER_NAME=$(kubectl config get-contexts "$context" | awk '{print $3}' | tail -n 1)
echo "Cluster name: ${CLUSTER_NAME}"

ENDPOINT=$(kubectl config view \
-o jsonpath="{.clusters[?(@.name == \"${CLUSTER_NAME}\")].cluster.server}")
echo "Endpoint: ${ENDPOINT}"

# Set up the config
echo -e "\\nPreparing k8s-${SERVICE_ACCOUNT_NAME}-${NAMESPACE}-conf"
echo -n "Setting a cluster entry in kubeconfig..."
kubectl config set-cluster "${CLUSTER_NAME}" \
--kubeconfig="${KUBECFG_FILE_NAME}" \
--server="${ENDPOINT}" \
--certificate-authority="sa_ca.crt" \
--embed-certs=true

echo -n "Setting token credentials entry in kubeconfig..."
kubectl config set-credentials \
"${SERVICE_ACCOUNT_NAME}-${NAMESPACE}-${CLUSTER_NAME}" \
--kubeconfig="${KUBECFG_FILE_NAME}" \
--token="${USER_TOKEN}"

echo -n "Setting a context entry in kubeconfig..."
kubectl config set-context \
"${SERVICE_ACCOUNT_NAME}-${NAMESPACE}-${CLUSTER_NAME}" \
--kubeconfig="${KUBECFG_FILE_NAME}" \
--cluster="${CLUSTER_NAME}" \
--user="${SERVICE_ACCOUNT_NAME}-${NAMESPACE}-${CLUSTER_NAME}" \
--namespace="${NAMESPACE}"

echo -n "Setting the current-context in the kubeconfig file..."
kubectl config use-context "${SERVICE_ACCOUNT_NAME}-${NAMESPACE}-${CLUSTER_NAME}" \
--kubeconfig="${KUBECFG_FILE_NAME}"

# remove temporal ca file
rm sa_ca.crt

echo -e "\\nConfiguration file '${KUBECFG_FILE_NAME}' done!!!!"
