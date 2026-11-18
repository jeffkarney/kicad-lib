package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeffk/kicad-lib/internal/config"
)

// PartMetadataResult holds enriched component metadata returned by a distributor lookup.
type PartMetadataResult struct {
	Provider     string
	MPN          string
	Manufacturer string
	Description  string
	Datasheet    string
	LCSCPart     string
	Attributes   map[string]string
}

// DistributorProvider defines the common interface for distributor metadata lookups.
type DistributorProvider interface {
	Name() string
	LookupByMPN(ctx context.Context, mpn string) (*PartMetadataResult, error)
}

// LookupMetadata queries configured distributor providers (Mouser, DigiKey, Nexar) and
// falls back to public EasyEDA/LCSC search if no API keys are configured or no match is found.
func LookupMetadata(ctx context.Context, cfg *config.Config, query string) (*PartMetadataResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("part number or query cannot be empty")
	}

	var providers []DistributorProvider
	if cfg != nil {
		if cfg.MouserAPIKey != "" {
			providers = append(providers, &MouserProvider{APIKey: cfg.MouserAPIKey})
		}
		if cfg.DigiKeyClientID != "" && cfg.DigiKeyClientSecret != "" {
			providers = append(providers, &DigiKeyProvider{
				ClientID:     cfg.DigiKeyClientID,
				ClientSecret: cfg.DigiKeyClientSecret,
			})
		}
		if cfg.NexarClientID != "" && cfg.NexarClientSecret != "" {
			providers = append(providers, &NexarProvider{
				ClientID:     cfg.NexarClientID,
				ClientSecret: cfg.NexarClientSecret,
			})
		}
	}
	// Always include public LCSC/EasyEDA lookup fallback
	providers = append(providers, &EasyEDAPublicProvider{})

	var errs []string
	for _, prov := range providers {
		res, err := prov.LookupByMPN(ctx, query)
		if err == nil && res != nil {
			return res, nil
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", prov.Name(), err))
		}
	}

	return nil, fmt.Errorf("no metadata match for %q (%s)", query, strings.Join(errs, "; "))
}

// MouserProvider queries the Mouser Search API v1.
type MouserProvider struct {
	APIKey  string
	BaseURL string
}

func (m *MouserProvider) Name() string { return "Mouser" }

func (m *MouserProvider) LookupByMPN(ctx context.Context, mpn string) (*PartMetadataResult, error) {
	base := m.BaseURL
	if base == "" {
		base = "https://api.mouser.com/api/v1/search/partnumber"
	}
	endpoint := base + "?apiKey=" + url.QueryEscape(m.APIKey)

	payload := map[string]any{
		"SearchByPartRequest": map[string]any{
			"mouserPartNumber":  mpn,
			"partSearchOptions": "string",
		},
	}
	raw, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var decoded struct {
		SearchResults struct {
			Parts []struct {
				ManufacturerPartNumber string `json:"ManufacturerPartNumber"`
				MouserPartNumber       string `json:"MouserPartNumber"`
				Manufacturer           string `json:"Manufacturer"`
				Description            string `json:"Description"`
				DataSheetUrl           string `json:"DataSheetUrl"`
				Category               string `json:"Category"`
				LifecycleStatus        string `json:"LifecycleStatus"`
				ROHSStatus             string `json:"ROHSStatus"`
				ProductAttributes      []struct {
					AttributeName  string `json:"AttributeName"`
					AttributeValue string `json:"AttributeValue"`
				} `json:"ProductAttributes"`
			} `json:"Parts"`
		} `json:"SearchResults"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.SearchResults.Parts) == 0 {
		return nil, errors.New("no parts found")
	}

	p := decoded.SearchResults.Parts[0]
	attrs := map[string]string{
		"Distributor": "Mouser",
		"MouserPN":    p.MouserPartNumber,
	}
	if p.Category != "" {
		attrs["Category"] = p.Category
	}
	if p.LifecycleStatus != "" {
		attrs["Lifecycle"] = p.LifecycleStatus
	}
	if p.ROHSStatus != "" {
		attrs["RoHS"] = p.ROHSStatus
	}
	for _, a := range p.ProductAttributes {
		if a.AttributeName != "" && a.AttributeValue != "" {
			attrs[a.AttributeName] = a.AttributeValue
		}
	}

	return &PartMetadataResult{
		Provider:     "Mouser",
		MPN:          p.ManufacturerPartNumber,
		Manufacturer: p.Manufacturer,
		Description:  p.Description,
		Datasheet:    p.DataSheetUrl,
		Attributes:   attrs,
	}, nil
}

// DigiKeyProvider queries DigiKey Product Information API v4 via OAuth2 client credentials.
type DigiKeyProvider struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	APIBaseURL   string
}

func (d *DigiKeyProvider) Name() string { return "DigiKey" }

func (d *DigiKeyProvider) LookupByMPN(ctx context.Context, mpn string) (*PartMetadataResult, error) {
	tokenURL := d.TokenURL
	if tokenURL == "" {
		tokenURL = "https://api.digikey.com/v1/oauth2/token"
	}
	apiBase := d.APIBaseURL
	if apiBase == "" {
		apiBase = "https://api.digikey.com/products/v4"
	}

	form := url.Values{
		"client_id":     {d.ClientID},
		"client_secret": {d.ClientSecret},
		"grant_type":    {"client_credentials"},
	}
	tReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	tReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	tResp, err := client.Do(tReq)
	if err != nil {
		return nil, err
	}
	defer tResp.Body.Close()
	if tResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token HTTP %d", tResp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tResp.Body).Decode(&tok); err != nil {
		return nil, err
	}
	if tok.AccessToken == "" {
		return nil, errors.New("empty access token")
	}

	detailURL := fmt.Sprintf("%s/search/%s/productdetails", strings.TrimSuffix(apiBase, "/"), url.PathEscape(mpn))
	pReq, err := http.NewRequestWithContext(ctx, http.MethodGet, detailURL, nil)
	if err != nil {
		return nil, err
	}
	pReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	pReq.Header.Set("X-DIGIKEY-Client-Id", d.ClientID)
	pReq.Header.Set("Accept", "application/json")

	pResp, err := client.Do(pReq)
	if err != nil {
		return nil, err
	}
	defer pResp.Body.Close()
	if pResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("productdetails HTTP %d", pResp.StatusCode)
	}

	var detail struct {
		Product struct {
			ManufacturerProductNumber string `json:"ManufacturerProductNumber"`
			DatasheetUrl              string `json:"DatasheetUrl"`
			Manufacturer              struct {
				Name string `json:"Name"`
			} `json:"Manufacturer"`
			Description struct {
				ProductDescription  string `json:"ProductDescription"`
				DetailedDescription string `json:"DetailedDescription"`
			} `json:"Description"`
			Parameters []struct {
				ParameterText string `json:"ParameterText"`
				ValueText     string `json:"ValueText"`
			} `json:"Parameters"`
		} `json:"Product"`
	}
	if err := json.NewDecoder(pResp.Body).Decode(&detail); err != nil {
		return nil, err
	}
	if detail.Product.ManufacturerProductNumber == "" {
		return nil, errors.New("empty DigiKey product response")
	}

	attrs := map[string]string{"Distributor": "DigiKey"}
	for _, p := range detail.Product.Parameters {
		if p.ParameterText != "" && p.ValueText != "" {
			attrs[p.ParameterText] = p.ValueText
		}
	}

	return &PartMetadataResult{
		Provider:     "DigiKey",
		MPN:          detail.Product.ManufacturerProductNumber,
		Manufacturer: detail.Product.Manufacturer.Name,
		Description:  firstNonEmpty(detail.Product.Description.DetailedDescription, detail.Product.Description.ProductDescription),
		Datasheet:    detail.Product.DatasheetUrl,
		Attributes:   attrs,
	}, nil
}

// NexarProvider queries Nexar/Octopart GraphQL API.
type NexarProvider struct {
	ClientID     string
	ClientSecret string
}

func (n *NexarProvider) Name() string { return "Nexar" }

func (n *NexarProvider) LookupByMPN(ctx context.Context, mpn string) (*PartMetadataResult, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {n.ClientID},
		"client_secret": {n.ClientSecret},
	}
	tReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://identity.nexar.com/connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	tReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	tResp, err := client.Do(tReq)
	if err != nil {
		return nil, err
	}
	defer tResp.Body.Close()
	if tResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Nexar token HTTP %d", tResp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tResp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		return nil, errors.New("failed to acquire Nexar token")
	}

	gql := map[string]any{
		"query":     `query ($q: String!) { supSearchMpn(q: $q, limit: 1) { results { part { mpn shortDescription manufacturer { name } bestDatasheet { url } specs { attribute { name } displayValue } } } } }`,
		"variables": map[string]any{"q": mpn},
	}
	raw, _ := json.Marshal(gql)
	gReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.nexar.com/graphql", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	gReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	gReq.Header.Set("Content-Type", "application/json")
	gResp, err := client.Do(gReq)
	if err != nil {
		return nil, err
	}
	defer gResp.Body.Close()
	if gResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Nexar GraphQL HTTP %d", gResp.StatusCode)
	}

	var out struct {
		Data struct {
			SupSearchMpn struct {
				Results []struct {
					Part struct {
						MPN              string `json:"mpn"`
						ShortDescription string `json:"shortDescription"`
						Manufacturer     struct {
							Name string `json:"name"`
						} `json:"manufacturer"`
						BestDatasheet struct {
							URL string `json:"url"`
						} `json:"bestDatasheet"`
						Specs []struct {
							Attribute struct {
								Name string `json:"name"`
							} `json:"attribute"`
							DisplayValue string `json:"displayValue"`
						} `json:"specs"`
					} `json:"part"`
				} `json:"results"`
			} `json:"supSearchMpn"`
		} `json:"data"`
	}
	if err := json.NewDecoder(gResp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data.SupSearchMpn.Results) == 0 {
		return nil, errors.New("no Nexar results")
	}
	p := out.Data.SupSearchMpn.Results[0].Part
	attrs := map[string]string{"Distributor": "Nexar"}
	for _, s := range p.Specs {
		if s.Attribute.Name != "" && s.DisplayValue != "" {
			attrs[s.Attribute.Name] = s.DisplayValue
		}
	}
	return &PartMetadataResult{
		Provider:     "Nexar",
		MPN:          p.MPN,
		Manufacturer: p.Manufacturer.Name,
		Description:  p.ShortDescription,
		Datasheet:    p.BestDatasheet.URL,
		Attributes:   attrs,
	}, nil
}

// EasyEDAPublicProvider looks up component metadata using LCSC C-numbers or EasyEDA public search without requiring API keys.
type EasyEDAPublicProvider struct {
	SearchURL string
}

func (e *EasyEDAPublicProvider) Name() string { return "EasyEDA/LCSC" }

func (e *EasyEDAPublicProvider) LookupByMPN(ctx context.Context, query string) (*PartMetadataResult, error) {
	if norm := normalizeLCSCID(query); norm != "" {
		cand, err := FetchLCSCPart(ctx, norm, "")
		if err == nil && cand != nil {
			return &PartMetadataResult{
				Provider:     "LCSC/EasyEDA",
				MPN:          cand.MPN,
				Manufacturer: cand.Manufacturer,
				Description:  cand.Description,
				Datasheet:    cand.Datasheet,
				LCSCPart:     cand.LCSCPart,
				Attributes:   cand.Metadata,
			}, nil
		}
	}

	searchURL := e.SearchURL
	if searchURL == "" {
		searchURL = "https://easyeda.com/api/components/search"
	}
	form := url.Values{
		"type":    {"3"},
		"wd":      {query},
		"version": {"6.4.19.5"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "kicad-lib-manager/1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var decoded struct {
		Success bool `json:"success"`
		Result  struct {
			Lists struct {
				LCSC []struct {
					Title       string `json:"title"`
					Description string `json:"description"`
					LCSC        struct {
						Number string `json:"number"`
					} `json:"lcsc"`
					DataStr struct {
						Head struct {
							CPara map[string]string `json:"c_para"`
						} `json:"head"`
					} `json:"dataStr"`
				} `json:"lcsc"`
			} `json:"lists"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Result.Lists.LCSC) == 0 {
		return nil, errors.New("no public LCSC matches")
	}
	first := decoded.Result.Lists.LCSC[0]
	cp := first.DataStr.Head.CPara
	attrs := make(map[string]string)
	for k, v := range cp {
		if v != "" {
			attrs[k] = v
		}
	}
	if first.LCSC.Number != "" {
		attrs["LCSC"] = first.LCSC.Number
	}
	return &PartMetadataResult{
		Provider:     "LCSC/EasyEDA",
		MPN:          firstNonEmpty(cp["Manufacturer Part"], first.Title),
		Manufacturer: firstNonEmpty(cp["Manufacturer"], cp["BOM_Manufacturer"]),
		Description:  firstNonEmpty(first.Description, cp["Description"]),
		Datasheet:    firstNonEmpty(cp["link"], cp["Datasheet"]),
		LCSCPart:     first.LCSC.Number,
		Attributes:   attrs,
	}, nil
}
