/**
 * Copyright 2020 IBM Corp.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package iam ...
package iam

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/IBM-Cloud/ibm-cloud-cli-sdk/common/rest"
	"github.com/IBM/ibmcloud-volume-interface/config"
	util "github.com/IBM/ibmcloud-volume-interface/lib/utils"
)

// tokenExchangeService ...
type tokenExchangeService struct {
	authConfig *AuthConfiguration
	httpClient *http.Client
}

// AuthConfiguration ...
type AuthConfiguration struct {
	IamURL          string
	IamClientID     string
	IamClientSecret string
}

// TokenExchangeService ...
var _ TokenExchangeService = &tokenExchangeService{}

// NewTokenExchangeServiceWithClient ...
func NewTokenExchangeServiceWithClient(authConfig *AuthConfiguration, httpClient *http.Client) (TokenExchangeService, error) {
	return &tokenExchangeService{
		authConfig: authConfig,
		httpClient: httpClient,
	}, nil
}

// NewTokenExchangeService ...
func NewTokenExchangeService(authConfig *AuthConfiguration) (TokenExchangeService, error) {
	httpClient, err := config.GeneralCAHttpClient()
	if err != nil {
		return nil, err
	}
	return &tokenExchangeService{
		authConfig: authConfig,
		httpClient: httpClient,
	}, nil
}

// tokenExchangeRequest ...
type tokenExchangeRequest struct {
	tes          *tokenExchangeService
	request      *rest.Request
	client       *rest.Client
	logger       *zap.Logger
	errorRetrier *util.ErrorRetrier
}

// tokenExchangeResponse ...
type tokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
	ImsToken    string `json:"ims_token"`
	ImsUserID   int    `json:"ims_user_id"`
}

// ExchangeRefreshTokenForAccessToken ...
func (tes *tokenExchangeService) ExchangeRefreshTokenForAccessToken(refreshToken string, logger *zap.Logger) (*AccessToken, error) {
	r := tes.newTokenExchangeRequest(logger)

	r.request.Field("grant_type", "refresh_token")
	r.request.Field("refresh_token", refreshToken)

	return r.exchangeForAccessToken()
}

// ExchangeAccessTokenForIMSToken ...
func (tes *tokenExchangeService) ExchangeAccessTokenForIMSToken(accessToken AccessToken, logger *zap.Logger) (*IMSToken, error) {
	r := tes.newTokenExchangeRequest(logger)

	r.request.Field("grant_type", "urn:ibm:params:oauth:grant-type:derive")
	r.request.Field("response_type", "ims_portal")
	r.request.Field("access_token", accessToken.Token)

	return r.exchangeForIMSToken()
}

// ExchangeIAMAPIKeyForIMSToken ...
func (tes *tokenExchangeService) ExchangeIAMAPIKeyForIMSToken(iamAPIKey string, logger *zap.Logger) (*IMSToken, error) {
	r := tes.newTokenExchangeRequest(logger)

	r.request.Field("grant_type", "urn:ibm:params:oauth:grant-type:apikey")
	r.request.Field("response_type", "ims_portal")
	r.request.Field("apikey", iamAPIKey)

	return r.exchangeForIMSToken()
}

// ExchangeIAMAPIKeyForAccessToken ...
func (tes *tokenExchangeService) ExchangeIAMAPIKeyForAccessToken(iamAPIKey string, logger *zap.Logger) (*AccessToken, error) {
	r := tes.newTokenExchangeRequest(logger)

	r.request.Field("grant_type", "urn:ibm:params:oauth:grant-type:apikey")
	r.request.Field("apikey", iamAPIKey)

	return r.exchangeForAccessToken()
}

// exchangeForAccessToken ...
func (r *tokenExchangeRequest) exchangeForAccessToken() (*AccessToken, error) {
	var iamResp *tokenExchangeResponse
	var err error
	err = r.errorRetrier.ErrorRetry(func() (error, bool) {
		iamResp, err = r.sendTokenExchangeRequest()
		return err, !IsConnectionError(err) // Skip rettry if its not connection error
	})
	if err != nil {
		return nil, err
	}
	return &AccessToken{Token: iamResp.AccessToken}, nil
}

// exchangeForIMSToken ...
func (r *tokenExchangeRequest) exchangeForIMSToken() (*IMSToken, error) {
	var iamResp *tokenExchangeResponse
	var err error
	err = r.errorRetrier.ErrorRetry(func() (error, bool) {
		iamResp, err = r.sendTokenExchangeRequest()
		return err, !IsConnectionError(err)
	})

	if err != nil {
		return nil, err
	}
	return &IMSToken{
		UserID: iamResp.ImsUserID,
		Token:  iamResp.ImsToken,
	}, nil
}

// newTokenExchangeRequest ...
func (tes *tokenExchangeService) newTokenExchangeRequest(logger *zap.Logger) *tokenExchangeRequest {
	client := rest.NewClient()
	client.HTTPClient = tes.httpClient
	retyrInterval, _ := time.ParseDuration("3s")
	return &tokenExchangeRequest{
		tes:          tes,
		request:      rest.PostRequest(fmt.Sprintf("%s/oidc/token", tes.authConfig.IamURL)),
		client:       client,
		logger:       logger,
		errorRetrier: util.NewErrorRetrier(40, retyrInterval, logger),
	}
}

// UpdateAPIKey ...
func (tes *tokenExchangeService) UpdateAPIKey(apiKey string, logger *zap.Logger) error {
	// This method need not be implemented
	return nil
}

// sendTokenExchangeRequest ...
func (r *tokenExchangeRequest) sendTokenExchangeRequest() (*tokenExchangeResponse, error) {
	// Set headers
	basicAuth := fmt.Sprintf("%s:%s", r.tes.authConfig.IamClientID, r.tes.authConfig.IamClientSecret)
	r.request.Set("Authorization", fmt.Sprintf("Basic %s", base64.StdEncoding.EncodeToString([]byte(basicAuth))))
	r.request.Set("Accept", "application/json")

	// Make the request
	var successV tokenExchangeResponse
	var errorV = struct {
		ErrorMessage string `json:"errorMessage"`
		ErrorType    string `json:"errorCode"`
		ErrorDetails string `json:"errorDetails"`
		Requirements struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		} `json:"requirements"`
	}{}

	r.logger.Info("Sending IAM token exchange request")
	r.logger.Info("Request is:=================", zap.Reflect("Request", r.request))
	resp, err := r.client.Do(r.request, &successV, &errorV)

	if err != nil {
		r.logger.Error("IAM token exchange request failed", zap.Reflect("Response", resp), zap.Error(err))

		// TODO Handle timeout here?

		return nil,
			util.NewError("ErrorUnclassified",
				"IAM token exchange request failed", err)
	}

	if resp != nil && resp.StatusCode == 200 {
		r.logger.Debug("IAM token exchange request successful")
		return &successV, nil
	}

	defer resp.Body.Close()

	// TODO Check other status code values? (but be careful not to mask the reason codes, below)

	if errorV.ErrorMessage != "" {
		r.logger.Error("IAM token exchange request failed with message",
			zap.Int("StatusCode", resp.StatusCode),
			zap.String("ErrorMessage:", errorV.ErrorMessage),
			zap.String("ErrorType:", errorV.ErrorType),
			zap.Reflect("Error", errorV))

		err := util.NewError("ErrorFailedTokenExchange",
			"IAM token exchange request failed: "+errorV.ErrorMessage,
			errors.New(errorV.ErrorDetails+" "+errorV.Requirements.Code+": "+errorV.Requirements.Error))

		if errorV.Requirements.Code == "SoftLayer_Exception_User_Customer_AccountLocked" {
			err = util.NewError("ErrorProviderAccountTemporarilyLocked",
				"Infrastructure account is temporarily locked", err)
		}

		return nil, err
	}

	r.logger.Error("Unexpected IAM token exchange response",
		zap.Int("StatusCode", resp.StatusCode), zap.Reflect("Response", resp))

	return nil,
		util.NewError("ErrorUnclassified",
			"Unexpected IAM token exchange response")
}

// IsConnectionError ...
func IsConnectionError(err error) bool {
	if err != nil {
		wrappedErrors := util.ErrorDeepUnwrapString(err)
		// wrapped error contains actual backend error
		for _, werr := range wrappedErrors {
			if strings.Contains(werr, "tcp") {
				// if  error contains "tcp" string, its connection error
				return true
			}
		}
	}
	return false
}

// String returns a pointer to the string value provided
func String(v string) *string {
	return &v
}
