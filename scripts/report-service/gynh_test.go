package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// The admin tier moved 10 → 50: the main service's admin (10) and pre-move
// tokens must still pass admin gates, while supplier_gynh (11) reaches only
// the gynh routes.
func TestAdminTierAndGynhGates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	status := func(gate gin.HandlerFunc, role int) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set("role", role)
		gate(c)
		if !c.IsAborted() {
			return http.StatusOK
		}
		return w.Code
	}
	cases := []struct {
		name      string
		role      int
		adminGate int
		gynhGate  int
	}{
		{"main-service admin", normalizeRole(10), http.StatusOK, http.StatusOK},
		{"admin", minAdminRole, http.StatusOK, http.StatusOK},
		{"super admin", normalizeRole(minSuperAdminRole), http.StatusOK, http.StatusOK},
		{"supplier_gynh", normalizeRole(minSupplierGynhRole), http.StatusForbidden, http.StatusOK},
		{"supplier_02", minSupplierRole02, http.StatusForbidden, http.StatusForbidden},
		{"user", normalizeRole(minUserRole), http.StatusForbidden, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.adminGate, status(requireRole(minAdminRole), tc.role))
			assert.Equal(t, tc.gynhGate, status(requireRoleOrSupplierGynh(minAdminRole), tc.role))
		})
	}
}
