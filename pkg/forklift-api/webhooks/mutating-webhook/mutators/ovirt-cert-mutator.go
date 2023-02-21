package mutators

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/konveyor/forklift-controller/pkg/forklift-api/webhooks/util"
	"github.com/konveyor/forklift-controller/pkg/lib/logging"
	admissionv1 "k8s.io/api/admission/v1beta1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var log = logging.WithName("mutator")

type OvirtCertMutator struct {
}

func (mutator *OvirtCertMutator) Mutate(ar *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	log.Info("secret mutator was called")
	raw := ar.Request.Object.Raw
	secret := &core.Secret{}
	err := json.Unmarshal(raw, secret)
	if err != nil {
		log.Error(err, "mutating webhook error")
		util.ToAdmissionResponseError(err)
	}

	insecure, err := strconv.ParseBool(string(secret.Data["insecureSkipVerify"]))
	if err != nil {
		log.Error(err, "mutating webhook URL parsing error")
		return util.ToAdmissionResponseError(err)
	}

	if providerType, ok := secret.GetLabels()["createdForProviderType"]; ok && providerType == "ovirt" && !insecure {

		url, err := url.Parse(string(secret.Data["url"]))
		if err != nil {
			log.Error(err, "mutating webhook URL parsing error")
			util.ToAdmissionResponseError(err)
		}

		certPool := x509.NewCertPool()
		certPool.AppendCertsFromPEM(secret.Data["cacert"])
		certUrl := fmt.Sprint(url.Scheme, "://", url.Host, "/ovirt-engine/services/pki-resource?resource=ca-certificate&format=X509-PEM-CA")
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: certPool}
		response, err := http.Get(certUrl)
		if err != nil {
			log.Error(err, "mutating webhook error")
			util.ToAdmissionResponseError(err)
		}

		b, err := io.ReadAll(response.Body)
		if err != nil {
			log.Error(err, "mutating webhook error")
			util.ToAdmissionResponseError(err)
		}

		//check if the CA included in the secrete provided by the user and update it if needed
		if !strings.Contains(string(secret.Data["cacert"]), string(b)) {
			secret.Data["cacert"] = append(secret.Data["cacert"], b...)
			secret.Labels["ca-cert-updated"] = "true"
			log.Info("Engine CA certificate was missing, updating the secret")
		}

		patchBytes, err := util.GeneratePatchPayload(
			util.PatchOperation{
				Op:    "replace",
				Path:  "/data",
				Value: secret.Data,
			},
			util.PatchOperation{
				Op:    "replace",
				Path:  "/metadata/labels",
				Value: secret.Labels,
			},
		)

		if err != nil {
			log.Error(err, "mutating webhook error")
			util.ToAdmissionResponseError(err)
		}

		jsonPatchType := admissionv1.PatchTypeJSONPatch
		return &admissionv1.AdmissionResponse{
			Allowed:   true,
			Patch:     patchBytes,
			PatchType: &jsonPatchType,
		}
	}

	// Response for other providers type or insecure mode
	return &admissionv1.AdmissionResponse{
		Allowed: true,
		Result: &metav1.Status{
			Message: "Certificate retrieval is not required, passing ",
			Code:    http.StatusOK,
		},
	}
}
