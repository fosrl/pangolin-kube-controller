#!/usr/bin/env bash
set -euo pipefail

PROGRAM="$(basename "$0")"

die() {
	echo "[$PROGRAM] error: $*" >&2
	exit 1
	return 0 # explicit for linters (unreachable after exit)
}

log() {
	echo "[$PROGRAM] $*"
	return 0
}

usage() {
	cat <<'USAGE'
Usage: hack/scripts/release.sh --version <semver> --image <image> [options]

Required arguments:
  --version <semver>        SemVer 2.0.0 version (e.g. 1.2.3, 1.4.0-rc.1)
  --image <name>            OCI image reference without tag (repeat for multiple registries)

Options:
  --platforms <list>        Target platforms for docker buildx (default: linux/amd64,linux/arm64)
  --publish-latest          Also push a `latest` tag (disabled for pre-releases)
  --publish-minor           Also push a `major.minor` tag (disabled for pre-releases)
  --sbom-path <file>        Path to write SBOM (default: dist/release/sbom-<version>.spdx.json)
  --trivy-format <fmt>      Trivy output format (table, sarif, json; default: table)
  --trivy-output <file>     Path to write Trivy scan report (default depends on format)
  --trivy-severity <list>   Severity filter for Trivy (default: CRITICAL,HIGH)
  --trivy-exit-on-findings  Exit with code 1 if Trivy finds selected severities
  --no-trivy-ignore-unfixed Include unfixed vulnerabilities in scan results
  --output-file <file>      Write key=value outputs to file for CI consumption
  --cosign-key <keyref>     Cosign key reference (file path or env://). If set, keyed signing is added to keyless/OIDC
  --skip-sign               Skip signing (not recommended)
  --skip-sbom               Skip SBOM generation
  --skip-scan               Skip Trivy vulnerability scan
  --sign-tags               Sign tags in addition to digests (default signs digests only)
  --no-sign-tags            Only sign digests (default)
  --context <dir>           Build context (default: .)
  --help                    Show this message

Environment variables:
  IMAGE_TITLE, IMAGE_SOURCE, IMAGE_DESCRIPTION, IMAGE_DOCUMENTATION,
  IMAGE_LICENSE may be set to override OCI labels.
  ARTIFACT_DIR (default: dist/release) controls where SBOM/scan results are stored.
  COSIGN_PASSWORD may be required when using password protected keys.
  BUILDX_BUILDER (default: pangolin-release-$$) sets the buildx builder instance name.
USAGE
	return 0
}

require_cmd() {
	local cmd="$1"
	command -v "$cmd" >/dev/null 2>&1 || die "missing required command '$cmd'"
	return 0
}

# Source shared SemVer constants
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/scripts/semver-constants.sh
source "${SCRIPT_DIR}/semver-constants.sh"

VERSION=""
IMAGES=()
PLATFORMS="linux/amd64,linux/arm64"
PUBLISH_LATEST=false
PUBLISH_MINOR=false
SBOM_PATH=""
TRIVY_FORMAT="table"
TRIVY_OUTPUT=""
TRIVY_SEVERITY="CRITICAL,HIGH"
TRIVY_EXIT_ON_FINDINGS=0
TRIVY_IGNORE_UNFIXED=1
OUTPUT_FILE=""
COSIGN_KEY=""
SKIP_SIGN=false
SKIP_SBOM=false
SKIP_SCAN=false
# Default: only sign digests. Tag signing can be enabled explicitly if needed.
SIGN_TAGS=false
CONTEXT="."

while [[ $# -gt 0 ]]; do
	case "$1" in
	--version)
		VERSION="$2"
		shift 2
		;;
	--image)
		IMAGES+=("$2")
		shift 2
		;;
	--platforms)
		PLATFORMS="$2"
		shift 2
		;;
	--publish-latest)
		PUBLISH_LATEST=true
		shift
		;;
	--publish-minor)
		PUBLISH_MINOR=true
		shift
		;;
	--sbom-path)
		SBOM_PATH="$2"
		shift 2
		;;
	--trivy-format)
		TRIVY_FORMAT="$2"
		shift 2
		;;
	--trivy-output)
		TRIVY_OUTPUT="$2"
		shift 2
		;;
	--trivy-severity)
		TRIVY_SEVERITY="$2"
		shift 2
		;;
	--trivy-exit-on-findings)
		TRIVY_EXIT_ON_FINDINGS=1
		shift
		;;
	--no-trivy-ignore-unfixed)
		TRIVY_IGNORE_UNFIXED=0
		shift
		;;
	--output-file)
		OUTPUT_FILE="$2"
		shift 2
		;;
	--cosign-key)
		COSIGN_KEY="$2"
		shift 2
		;;
	--skip-sign)
		SKIP_SIGN=true
		shift
		;;
	--skip-sbom)
		SKIP_SBOM=true
		shift
		;;
	--skip-scan)
		SKIP_SCAN=true
		shift
		;;
	--sign-tags)
		SIGN_TAGS=true
		shift
		;;
	--no-sign-tags)
		SIGN_TAGS=false
		shift
		;;
	--context)
		CONTEXT="$2"
		shift 2
		;;
	--help)
		usage
		exit 0
		;;
	*)
		echo "[$PROGRAM] unknown argument: $1" >&2
		usage
		exit 1
		;;
	esac
done

[[ -n $VERSION ]] || die "--version is required"
[[ ${#IMAGES[@]} -gt 0 ]] || die "at least one --image is required"

if [[ ! -d $CONTEXT ]]; then
	die "context '$CONTEXT' does not exist"
fi

DOCKERFILE_PATH=${DOCKERFILE_PATH:-}
if [[ -z $DOCKERFILE_PATH ]]; then
	DOCKERFILE_PATH="${CONTEXT%/}/Dockerfile"
fi
if [[ ! -f $DOCKERFILE_PATH ]]; then
	die "Dockerfile not found at '$DOCKERFILE_PATH'"
fi

require_cmd docker
require_cmd jq

if ! $SKIP_SIGN; then
	require_cmd cosign
fi

if ! $SKIP_SBOM || ! $SKIP_SCAN; then
	require_cmd trivy
fi

ARTIFACT_DIR=${ARTIFACT_DIR:-dist/release}
mkdir -p "$ARTIFACT_DIR"

if [[ -z $SBOM_PATH ]]; then
	SBOM_PATH="$ARTIFACT_DIR/sbom-${VERSION}.spdx.json"
fi

if [[ -z $TRIVY_OUTPUT ]]; then
	case "$TRIVY_FORMAT" in
	sarif)
		TRIVY_OUTPUT="$ARTIFACT_DIR/trivy-${VERSION}.sarif"
		;;
	json)
		TRIVY_OUTPUT="$ARTIFACT_DIR/trivy-${VERSION}.json"
		;;
	*)
		TRIVY_OUTPUT="$ARTIFACT_DIR/trivy-${VERSION}.txt"
		;;
	esac
fi

MAJOR=""
MINOR=""
PATCH=""
PRERELEASE=""
BUILD_METADATA=""
if [[ $VERSION =~ $SEMVER_REGEX ]]; then
	MAJOR="${BASH_REMATCH[1]}"
	MINOR="${BASH_REMATCH[2]}"
	PATCH="${BASH_REMATCH[3]}"
	PRERELEASE="${BASH_REMATCH[4]:-}"
	BUILD_METADATA="${BASH_REMATCH[7]:-}"
else
	die "version '$VERSION' is not valid SemVer 2.0.0"
fi

if [[ -n $PRERELEASE ]]; then
	if $PUBLISH_LATEST; then
		log "pre-release detected (${PRERELEASE}); skipping 'latest' tag"
		PUBLISH_LATEST=false
	fi
	if $PUBLISH_MINOR; then
		log "pre-release detected (${PRERELEASE}); skipping 'major.minor' tag"
		PUBLISH_MINOR=false
	fi
fi

BUILD_DATE="${IMAGE_CREATED:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
GIT_REVISION="$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
IMAGE_TITLE=${IMAGE_TITLE:-pangolin-kube-controller}
IMAGE_SOURCE=${IMAGE_SOURCE:-$(git config --get remote.origin.url 2>/dev/null || echo "")}
IMAGE_DESCRIPTION=${IMAGE_DESCRIPTION:-Pangolin controller}
IMAGE_DOCUMENTATION=${IMAGE_DOCUMENTATION:-$IMAGE_SOURCE}
IMAGE_LICENSE=${IMAGE_LICENSE:-NOASSERTION}

TAGS=()
SUMMARY_TAGS=()
IMAGES_LOWER=()
for image in "${IMAGES[@]}"; do
	image_lower="${image,,}"
	TAGS+=("-t" "${image_lower}:${VERSION}")
	SUMMARY_TAGS+=("${image_lower}:${VERSION}")
	if $PUBLISH_MINOR && [[ -n $MAJOR && -n $MINOR ]]; then
		TAGS+=("-t" "${image_lower}:${MAJOR}.${MINOR}")
		SUMMARY_TAGS+=("${image_lower}:${MAJOR}.${MINOR}")
	fi
	if $PUBLISH_LATEST; then
		TAGS+=("-t" "${image_lower}:latest")
		SUMMARY_TAGS+=("${image_lower}:latest")
	fi
	SUMMARY_TAGS+=("${image_lower}@<digest>")
	IMAGES_LOWER+=("${image_lower}")
done

METADATA_FILE="$(mktemp)"
cleanup() {
	rm -f "$METADATA_FILE"
	return 0
}
trap cleanup EXIT

# Use configurable builder name with unique suffix to avoid parallel execution conflicts
BUILDX_BUILDER="${BUILDX_BUILDER:-pangolin-release-$$}"

# Ensure the configured buildx builder exists and bootstrap the named builder.
# Inspect the specific builder name (treat non-zero exit as not found) so we
# create and initialize the exact builder instance instead of relying on any
# active/default builder.
if ! docker buildx inspect "${BUILDX_BUILDER}" >/dev/null 2>&1; then
	log "buildx builder '${BUILDX_BUILDER}' not found, creating and using it"
	docker buildx create --name "${BUILDX_BUILDER}" --use >/dev/null
fi
docker buildx inspect "${BUILDX_BUILDER}" --bootstrap >/dev/null
# Ensure the named builder is selected for subsequent buildx operations
docker buildx use "${BUILDX_BUILDER}" >/dev/null

LABEL_FLAG="--label"
LABELS=(
	"$LABEL_FLAG" "org.opencontainers.image.created=${BUILD_DATE}"
	"$LABEL_FLAG" "org.opencontainers.image.version=${VERSION}"
	"$LABEL_FLAG" "org.opencontainers.image.revision=${GIT_REVISION}"
)

if [[ -n $IMAGE_SOURCE ]]; then
	LABELS+=("$LABEL_FLAG" "org.opencontainers.image.source=${IMAGE_SOURCE}")
	LABELS+=("$LABEL_FLAG" "org.opencontainers.image.url=${IMAGE_SOURCE}")
fi
if [[ -n $IMAGE_DOCUMENTATION ]]; then
	LABELS+=("$LABEL_FLAG" "org.opencontainers.image.documentation=${IMAGE_DOCUMENTATION}")
fi
if [[ -n $IMAGE_DESCRIPTION ]]; then
	LABELS+=("$LABEL_FLAG" "org.opencontainers.image.description=${IMAGE_DESCRIPTION}")
fi
if [[ -n $IMAGE_LICENSE ]]; then
	LABELS+=("$LABEL_FLAG" "org.opencontainers.image.licenses=${IMAGE_LICENSE}")
fi

BUILD_ARGS=(
	"--build-arg" "VERSION=${VERSION}"
	"--build-arg" "GIT_REVISION=${GIT_REVISION}"
	"--build-arg" "BUILD_DATE=${BUILD_DATE}"
)

BUILD_CMD=(docker buildx build)
BUILD_CMD+=("--platform" "${PLATFORMS}")
BUILD_CMD+=("--push")
BUILD_CMD+=("--provenance=true")
BUILD_CMD+=("--metadata-file" "${METADATA_FILE}")
BUILD_CMD+=("--progress" "plain")
BUILD_CMD+=("--file" "${DOCKERFILE_PATH}")
BUILD_CMD+=("$LABEL_FLAG" "org.opencontainers.image.title=${IMAGE_TITLE}")
for item in "${LABELS[@]}"; do
	BUILD_CMD+=("${item}")
done
for arg in "${BUILD_ARGS[@]}"; do
	BUILD_CMD+=("${arg}")
done
for tag in "${TAGS[@]}"; do
	BUILD_CMD+=("${tag}")
done
BUILD_CMD+=("${CONTEXT}")

log "building and pushing images for ${VERSION}"
"${BUILD_CMD[@]}"

get_digest() {
	local ref="$1"
	local digest=""

	# Primary: structured buildx output (robust for multi-arch images)
	digest="$(docker buildx imagetools inspect "$ref" --format '{{.Manifest.Digest}}' 2>/dev/null || true)"

	# Fallback: plain text parsing
	if ! [[ $digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
		digest="$(docker buildx imagetools inspect "$ref" 2>/dev/null | awk '/^Digest:/ {print $2; exit}' || true)"
	fi

	if ! [[ $digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
		echo "failed to resolve digest for ${ref}" >&2
		docker buildx imagetools inspect "$ref" || true
		return 1
	fi

	echo "$digest"
	return 0
}

# Resolve the digest from the registry to avoid depending on local build metadata.
DIGEST="$(get_digest "${IMAGES_LOWER[0]}:${VERSION}")" || die "failed to resolve image digest from registry"

PRIMARY_IMAGE="${IMAGES_LOWER[0]}"
PRIMARY_REF="${PRIMARY_IMAGE}@${DIGEST}"

SUMMARY_ACTUAL=()
for entry in "${SUMMARY_TAGS[@]}"; do
	SUMMARY_ACTUAL+=("${entry//<digest>/${DIGEST}}")
done
SUMMARY_TAGS=("${SUMMARY_ACTUAL[@]}")

log "image digest: ${PRIMARY_REF}"

# Use a shared cache directory for Trivy to avoid redundant image downloads
TRIVY_CACHE_DIR="$ARTIFACT_DIR/.trivy-cache"
mkdir -p "$TRIVY_CACHE_DIR"

if ! $SKIP_SBOM; then
	mkdir -p "$(dirname "$SBOM_PATH")"
	log "generating SBOM at ${SBOM_PATH}"
	TMP_SBOM="${SBOM_PATH}.tmp"
	trivy image --quiet --cache-dir "${TRIVY_CACHE_DIR}" --format spdx-json --output "${TMP_SBOM}" "${PRIMARY_REF}"
	jq -c . "${TMP_SBOM}" >"${SBOM_PATH}"
	rm -f "${TMP_SBOM}"
fi

if ! $SKIP_SCAN; then
	mkdir -p "$(dirname "$TRIVY_OUTPUT")"
	log "running Trivy vulnerability scan (${TRIVY_FORMAT})"
	TRIVY_ARGS=(image --cache-dir "${TRIVY_CACHE_DIR}" --severity "${TRIVY_SEVERITY}" --format "${TRIVY_FORMAT}")
	if [[ ${TRIVY_FORMAT} != "table" ]]; then
		TRIVY_ARGS+=(--output "${TRIVY_OUTPUT}")
	fi
	if [[ $TRIVY_IGNORE_UNFIXED -eq 1 ]]; then
		TRIVY_ARGS+=(--ignore-unfixed)
	fi
	if [[ $TRIVY_EXIT_ON_FINDINGS -eq 1 ]]; then
		TRIVY_ARGS+=(--exit-code 1)
	else
		TRIVY_ARGS+=(--exit-code 0)
	fi
	TRIVY_ARGS+=("${PRIMARY_REF}")
	if [[ ${TRIVY_FORMAT} == "table" ]]; then
		set +e
		trivy "${TRIVY_ARGS[@]}" | tee "${TRIVY_OUTPUT}"
		trivy_exit_code=${PIPESTATUS[0]}
		set -e
	else
		set +e
		trivy "${TRIVY_ARGS[@]}"
		trivy_exit_code=$?
		set -e
	fi
	if [[ ${trivy_exit_code:-0} -ne 0 ]]; then
		if [[ $TRIVY_EXIT_ON_FINDINGS -eq 1 ]]; then
			die "Trivy scan found vulnerabilities (exit code: ${trivy_exit_code})"
		else
			die "Trivy scan failed (exit code: ${trivy_exit_code})"
		fi
	fi
fi

if ! $SKIP_SIGN; then
	export COSIGN_YES=1
	export COSIGN_DOCKER_MEDIA_TYPES=1
	# sign_ref signs an image reference.
	# We only use --recursive for digest refs. This matches the more reliable
	# pattern used in the reference pipeline and avoids redundant registry writes
	# for tag-based signatures.
	sign_ref() {
		local target="$1"
		local recursive_args=()
		if [[ $target == *@sha256:* ]]; then
			recursive_args=(--recursive)
		fi

		# Keyless signature
		cosign sign --yes "${recursive_args[@]}" "$target"

		# Keyed signature
		if [[ -n $COSIGN_KEY ]]; then
			cosign sign --key "$COSIGN_KEY" --yes "${recursive_args[@]}" "$target"
		fi
		return 0
	}
	attest_sbom() {
		local target="$1"
		# Keyless attestation
		if [[ -n ${SBOM_PATH:-} ]]; then
			cosign attest --type spdxjson --predicate "$SBOM_PATH" "$target"
		fi

		# Keyed attestation
		if [[ -n $COSIGN_KEY && -n ${SBOM_PATH:-} ]]; then
			cosign attest --key "$COSIGN_KEY" --type spdxjson --predicate "$SBOM_PATH" "$target"
		fi
		return 0
	}
	for image in "${IMAGES_LOWER[@]}"; do
		ref="${image}@${DIGEST}"
		log "signing ${ref}"
		sign_ref "$ref"

		# Optional tag signing remains available, but digest signing is the default.
		if $SIGN_TAGS; then
			log "signing ${image}:${VERSION}"
			sign_ref "${image}:${VERSION}"
			if $PUBLISH_MINOR && [[ -n $MAJOR && -n $MINOR ]]; then
				log "signing ${image}:${MAJOR}.${MINOR}"
				sign_ref "${image}:${MAJOR}.${MINOR}"
			fi
			if $PUBLISH_LATEST; then
				log "signing ${image}:latest"
				sign_ref "${image}:latest"
			fi
		fi
		if ! $SKIP_SBOM; then
			log "attesting SBOM for ${ref}"
			attest_sbom "$ref"
		fi
	done
fi

log "published tags:"
for entry in "${SUMMARY_TAGS[@]}"; do
	log "  - ${entry}"
done

if [[ -n $OUTPUT_FILE ]]; then
	{
		echo "VERSION=${VERSION}"
		echo "DIGEST=${DIGEST}"
		echo "PRIMARY_DIGEST_REF=${PRIMARY_REF}"
		# Only print SBOM_PATH when an SBOM was produced
		if [[ -n ${SBOM_PATH:-} && ! $SKIP_SBOM ]]; then
			echo "SBOM_PATH=${SBOM_PATH}"
		fi
		# Only print Trivy output path when a scan was run
		if [[ -n ${TRIVY_OUTPUT:-} && ! $SKIP_SCAN ]]; then
			echo "TRIVY_REPORT=${TRIVY_OUTPUT}"
		fi
		# Join image list with spaces without trailing whitespace
		echo "IMAGES=${IMAGES_LOWER[*]}"
	} >"$OUTPUT_FILE"
fi
