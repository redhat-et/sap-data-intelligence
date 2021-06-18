#!/usr/bin/env bash

# OCP Template for SDI Observer

set -euo pipefail

# namespace where SAP Data Intelligence is or will be installed
SDI_NAMESPACE=sdi
# namespace where SDI Observer is or will be installed; shall be different from SDI_NAMESPACE
NAMESPACE=sdi-observer
SLCB_NAMESPACE=sap-slcbridge
# SDI Observer will not do any modifications to the k8s resources, it will only print what would
# have been done
DRY_RUN=false
# if left unset, it will be determined from OCP server API
#OCP_MINOR_RELEASE=4.6
DEPLOY_SDI_REGISTRY=false
INJECT_CABUNDLE=true
MANAGE_VSYSTEM_ROUTE=true
#VSYSTEM_ROUTE_HOSTNAME=vsystem-<SDI_NAMESPACE>.<clustername>.<base_domain>
SDI_NODE_SELECTOR="node-role.kubernetes.io/sdi="


# There are three flavours of OCP Template:
# 1. ubi-build      (recommended, connected)
# 2. ubi-prebuilt   (disconnected/offline/air-gapped) - use pre-built images
# 3. custom-build   (best-effort-support)
FLAVOUR=ubi-build

# Required parameters for each template flavour:
# 1. ubi-build: set the following variable (use UBI8 for the base image)
#REDHAT_REGISTRY_SECRET_NAME=""
# 2. ubi-prebuilt
#SOURCE_IMAGE_PULL_SPEC=registry.centos.org/centos:8
#SOURCE_IMAGESTREAM_NAME=centos8
#SOURCE_IMAGESTREAM_TAG=latest
# 3. custom-build
# The image shall be first mirrored from the quay.io registry to a local container image registry.
# Then the below variable must be set accordingly. The %%OCP_MINOR_RELEASE%% macro will be
# replaced with the value of OCP_MINOR_RELEASE variable.
#IMAGE_PULL_SPEC=quay.io/miminar/sdi-observer:latest-ocp%%OCP_MINOR_RELEASE%%


# whether the observer shall deploy a container image registry in its NAMESPACE
DEPLOY_SDI_REGISTRY=false
#SDI_REGISTRY_STORAGE_CLASS_NAME=       # use the default sc unless set
# change to ReadWriteMany if supported by the storage class
# leave unset for script to decide in a best-effort manor
#SDI_REGISTRY_VOLUME_ACCESS_MODE=       # ReadWriteMany or ReadWriteOnce
SDI_REGISTRY_VOLUME_CAPACITY=120Gi
#SDI_REGISTRY_ROUTE_HOSTNAME=container-image-registry-<NAMESPACE>-apps.<cluster_name>.<base_domain>
SDI_REGISTRY_AUTHENTICATION=basic       # "none" disables the authentication
#SDI_REGISTRY_USERNAME=                 # auto-generated unless set
#SDI_REGISTRY_PASSWORD=                 # auto-generated unless set
#SDI_REGISTRY_HTPASSWD_SECRET_NAME=     # auto-generated unless set

INJECT_CABUNDLE=false
CABUNDLE_SECRET_NAME=openshift-ingress-operator/router-ca

# build the latest revision; change to a particular tag if needed (e.g. 0.1.13)
SDI_OBSERVER_GIT_REVISION=master
# uncomment to always use the git repository
# set to path/to/a/local/checkout to use a local file
# leave commented to autodecect (prefer local file, fallback to the remote git repository)
# NOTE: OCP build cannot use local checkout
#SDI_OBSERVER_REPOSITORY=https://github.com/redhat-sap/sap-data-intelligence

#################################################################################################
# DO NOT EDIT THE LINES BELOW
#################################################################################################

readonly gitRepo=https://github.com/redhat-sap/sap-data-intelligence

readonly commonEnvVars=(
    SDI_NAMESPACE
    NAMESPACE
    SLCB_NAMESPACE
    DRY_RUN
    OCP_MINOR_RELEASE
    MANAGE_VSYSTEM_ROUTE
    VSYSTEM_ROUTE_HOSTNAME
    SDI_NODE_SELECTOR

    INJECT_CABUNDLE
    CABUNDLE_SECRET_NAME
)

readonly registryEnvVars=(
    DEPLOY_SDI_REGISTRY
    SDI_REGISTRY_STORAGE_CLASS_NAME
    SDI_REGISTRY_VOLUME_ACCESS_MODE
    SDI_REGISTRY_VOLUME_CAPACITY
    SDI_REGISTRY_ROUTE_HOSTNAME
    SDI_REGISTRY_AUTHENTICATION
    SDI_REGISTRY_USERNAME
    SDI_REGISTRY_PASSWORD
    SDI_REGISTRY_HTPASSWD_SECRET_NAME
)

readonly buildEnvVars=(
    SDI_OBSERVER_REPOSITORY
    SDI_OBSERVER_GIT_REVISION
)

readonly rwxStorageClasses=(
    ocs-storagecluster-cephfs
)

declare -r -A envVarDefaults=(
    [IMAGE_PULL_SPEC]='quay.io/miminar/sdi-observer:latest-ocp%%OCP_MINOR_RELEASE%%'
)

envVars=( "${commonEnvVars[@]}" )

function join() { local IFS="$1"; shift; echo "$*"; }

case "${FLAVOUR:-ubi-build}" in
    ubi-build)
        envVars+=(
            REDHAT_REGISTRY_SECRET_NAME
            "${buildEnvVars[@]}"
            "${registryEnvVars[@]}"
        )
        template=ocp-template
        ;;
    ubi-prebuilt)
        envVars+=( IMAGE_PULL_SPEC )
        template=ocp-prebuilt-image-template
        ;;
    custom-build)
        envVars+=(
            SOURCE_IMAGE_PULL_SPEC
            SOURCE_IMAGESTREAM_NAME
            SOURCE_IMAGESTREAM_TAG
            "${buildEnvVars[@]}"
            "${registryEnvVars[@]}" 
        )
        template=ocp-custom-source-image-template
        ;;
    *)
        printf 'Unsupported FLAVOUR="%s", please choose one of:' "${FLAVOUR:-}" >&2
        printf ' ubi-build, ubi-prebuilt, custom-build\n' >&2
        exit 1
        ;;
esac

if [[ -z "${OCP_MINOR_RELEASE:-}" ]]; then
    ocpServerVersion="$(oc version | awk 'BEGIN {
        IGNORECASE=1
    }
    match($0, /^server\s*version:\s*([0-9]+.[0-9]+)/, a) {
        print a[1]
    }')"
    if [[ -n "${ocpServerVersion:-}" ]]; then
        OCP_MINOR_RELEASE="${ocpServerVersion}"
    else
        { printf '%s\n' \
            'Failed to determine the OCP server version!' \
            'Please either set the OCP_MINOR_RELEASE variable or ensure that you are' \
            'logged in to the cluster and that your user has cluster-reader role.'; \
        } >&2
        exit 1
    fi
fi

ocpClientVersion="$(oc version | awk 'BEGIN {
    IGNORECASE=1
}
match($0, /^client\s*version:\s*([0-9]+.[0-9]+)/, a) {
    print a[1]
}')"
minorMismatch="$(bc -l <<< 'define abs(i) {
    if (i < 0) return (-i)
    return (i)
}'" abs(${OCP_MINOR_RELEASE#*.} - ${ocpClientVersion#*.})")"

case "$minorMismatch" in
    0 | 1)
        ;;
    2)
        {
            printf 'WARNING: oc client version does not match the desired OCP'; \
            printf ' server version (%s =! %s)!\n' \
                "$ocpClientVersion" "$OCP_MINOR_RELEASE"
        } >&2
        ;;
    *)
        {
            printf 'ERROR: oc client version does not match the desired server OCP release'; \
            printf ' (%s =! %s)!\n' "$ocpClientVersion" "$OCP_MINOR_RELEASE"; \
            printf 'ERROR: Please download and use oc client matching the server minor'; \
            printf ' release %s.\n' "$OCP_MINOR_RELEASE"; \
        } >&2
        exit 1
        ;;
esac

sourceLocation="$gitRepo"
root="$(dirname "$(dirname "${BASH_SOURCE[0]}")")"
if [[ -n "${SDI_OBSERVER_REPOSITORY:-}" ]]; then
    sourceLocation="${SDI_OBSERVER_REPOSITORY:-}"
elif [[ -e "$root/observer/${template}.json" ]]; then
    sourceLocation="$root"
fi

args=( -f )
if [[ "${sourceLocation:-}" =~ ^https:// ]]; then
    args+=(
        "$(join / "$sourceLocation" \
            "${SDI_OBSERVER_GIT_REVISION:-master}" \
            "observer/${template}.json")"
    )
else
    args+=(
        "$(join / "$sourceLocation" \
            "observer/${template}.json")"
    )
fi

if [[ -z "${SDI_REGISTRY_VOLUME_ACCESS_MODE:-}" ]]; then
    if grep -F -x -q -f <(printf '%s\n' "${rwxStorageClasses[@]}") \
                <<<"${SDI_REGISTRY_STORAGE_CLASS_NAME:-}";
    then
        SDI_REGISTRY_VOLUME_ACCESS_MODE=ReadWriteMany
    fi

    tmpl="$(printf '%s' \
        '{{range $i, $sc := .items}}' \
            '{{with $mt := $sc.metadata}}' \
                '{{if $mt.annotations}}' \
                    '{{if eq "true" (index $mt.annotations' \
                        ' "storageclass.kubernetes.io/is-default-class")}}' \
                        '{{$mt.name}}{{"\n"}}' \
                    '{{end}}' \
                '{{end}}' \
            '{{end}}' \
        '{{end}}')"
    if grep -F -x -q -f <(printf '%s\n' "${rwxStorageClasses[@]}") \
        < <(oc get sc -o go-template="$tmpl");
    then
        SDI_REGISTRY_VOLUME_ACCESS_MODE=ReadWriteMany
    fi
fi

for var in "${envVars[@]}"; do
    eval 'value="${'"$var"':-}"'
    if [[ -z "${value:-}" ]]; then
        if [[ -z "${envVarDefaults[$var]:-}" ]]; then
            continue
        fi
        value="${envVarDefaults[$var]:-}"
    fi
    case "$var" in
        SDI_OBSERVER_REPOSITORY)
            if ! [[ "${value}" =~ ^(http|ftp|https):// ]]; then
                value="$gitRepo"
            fi
            ;;
        *IMAGE_PULL_SPEC*)
            value="${value//%%OCP_MINOR_RELEASE%%/$OCP_MINOR_RELEASE}"
            ;;
    esac
    args+=( "$var=$value" )
    printf '%s="%s"\n' "$var" "$value"
done
printf '\n'

# create namespaces if they do not exist yet
projects="$(printf 'project/%s\n' "$NAMESPACE" "$SDI_NAMESPACE" "$SLCB_NAMESPACE" | sort -u)"
grep -v -x -f <(xargs -r oc get -o jsonpath='{range .items[*]}project/{.metadata.name}{"\n"}{end}' \
            2>/dev/null <<<"$projects") <<<"$projects" | sed 's,^project.*/,,' | \
    xargs -n 1 -r oc create namespace ||:

oc process "${args[@]}" "$@" | oc apply -f - | grep -v -F \
    'Warning: oc apply should be used on resource created by'