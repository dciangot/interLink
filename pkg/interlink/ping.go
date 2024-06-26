package interlink

import (
	"net/http"
	"os"

	"github.com/containerd/containerd/log"
)

func (h *InterLinkHandler) Ping(w http.ResponseWriter, r *http.Request) {
	log.G(Ctx).Info("InterLink: received Ping call")
	w.WriteHeader(http.StatusOK)

	// 0 = KUBECONFIG already set
	// 1 = KUBECONFIG not set
	if os.Getenv("KUBECONFIG") != "" {
		w.Write([]byte("0"))
	} else {
		w.Write([]byte("1"))
	}
}
