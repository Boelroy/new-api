package supplierbilling

import (
	"errors"
	"net/http"
	"strconv"
)

var (
	errVendorOwnership = errors.New("Source channel belongs to a different supplier head")
	errVendorMapping   = errors.New("Source vendor mapping is invalid")
)

type VendorListItem struct {
	ID           int64  `json:"id"`
	SupplierID   int64  `json:"supplier_id"`
	Name         string `json:"name"`
	SourceStudio string `json:"source_studio"`
	KeyCount     int64  `json:"key_count"`
}

func (s *Server) listVendors(w http.ResponseWriter, r *http.Request) {
	var supplierID int64
	rawSupplierID := r.URL.Query().Get("supplier_id")
	if rawSupplierID != "" {
		var err error
		supplierID, err = strconv.ParseInt(rawSupplierID, 10, 64)
		if err != nil || supplierID <= 0 {
			s.fail(w, http.StatusBadRequest, "Select a supplier")
			return
		}
	}
	if currentUser(r).Role == RoleSupplier || supplierID != 0 {
		supplier, ok := s.targetSupplier(w, r, supplierID)
		if !ok {
			return
		}
		supplierID = supplier.ID
	}
	query := s.DB.Model(&Vendor{}).
		Select("sb_vendors.id, sb_vendors.supplier_id, sb_vendors.name, sb_vendors.source_studio, COUNT(sb_vendor_keys.id) AS key_count").
		Joins("LEFT JOIN sb_vendor_keys ON sb_vendor_keys.vendor_id = sb_vendors.id").
		Group("sb_vendors.id, sb_vendors.supplier_id, sb_vendors.name, sb_vendors.source_studio").
		Order("sb_vendors.name, sb_vendors.id")
	if supplierID != 0 {
		query = query.Where("sb_vendors.supplier_id = ?", supplierID)
	}
	items := []VendorListItem{}
	if err := query.Scan(&items).Error; err != nil {
		s.fail(w, http.StatusInternalServerError, "Database operation failed")
		return
	}
	s.write(w, http.StatusOK, items)
}
