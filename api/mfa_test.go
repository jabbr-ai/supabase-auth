package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/netlify/gotrue/conf"
	"github.com/netlify/gotrue/models"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type MFATestSuite struct {
	suite.Suite
	API        *API
	Config     *conf.Configuration
	instanceID uuid.UUID
}

func TestMFA(t *testing.T) {
	api, config, instanceID, err := setupAPIForTestForInstance()
	require.NoError(t, err)

	ts := &MFATestSuite{
		API:        api,
		Config:     config,
		instanceID: instanceID,
	}
	defer api.db.Close()

	suite.Run(t, ts)
}

func (ts *MFATestSuite) SetupTest() {
	models.TruncateAll(ts.API.db)

	// Create user
	u, err := models.NewUser(ts.instanceID, "123456789", "test@example.com", "password", ts.Config.JWT.Aud, nil)
	require.NoError(ts.T(), err, "Error creating test user model")
	require.NoError(ts.T(), ts.API.db.Create(u), "Error saving new test user")
	f, err := models.NewFactor(u, "testSimpleName", "testFactorID", "totp", models.FactorDisabledState, "secretkey")
	require.NoError(ts.T(), err, "Error creating test factor model")
	require.NoError(ts.T(), ts.API.db.Create(f), "Error saving new test factor")
}

func (ts *MFATestSuite) TestMFARecoveryCodeGeneration() {
	const expectedNumOfRecoveryCodes = 8
	user, err := models.FindUserByEmailAndAudience(ts.API.db, ts.instanceID, "test@example.com", ts.Config.JWT.Aud)
	ts.Require().NoError(err)
	require.NoError(ts.T(), user.EnableMFA(ts.API.db))

	token, err := generateAccessToken(user, time.Second*time.Duration(ts.Config.JWT.Exp), ts.Config.JWT.Secret)
	require.NoError(ts.T(), err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/mfa/%s/recovery_codes", user.ID), nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	ts.API.handler.ServeHTTP(w, req)
	require.Equal(ts.T(), http.StatusOK, w.Code)

	data := make(map[string]interface{})
	require.NoError(ts.T(), json.NewDecoder(w.Body).Decode(&data))

	recoveryCodes := data["recovery_codes"].([]interface{})
	require.Equal(ts.T(), expectedNumOfRecoveryCodes, len(recoveryCodes))
}

func (ts *MFATestSuite) TestEnrollFactor() {
	var cases = []struct {
		desc         string
		FriendlyName string
		FactorType   string
		Issuer       string
		MFAEnabled   bool
		expectedCode int
	}{
		{
			"TOTP: MFA is disabled",
			"",
			"totp",
			"supabase.com",
			false,
			http.StatusForbidden,
		},
		{
			"TOTP: Factor has friendly name",
			"bob",
			"totp",
			"supabase.com",
			true,
			http.StatusOK,
		},
		{
			"TOTP: Without simple name",
			"",
			"totp",
			"supabase.com",
			true,
			http.StatusOK,
		},
	}
	for _, c := range cases {
		ts.Run(c.desc, func() {
			var buffer bytes.Buffer
			require.NoError(ts.T(), json.NewEncoder(&buffer).Encode(map[string]string{"friendly_name": c.FriendlyName, "factor_type": c.FactorType, "issuer": c.Issuer}))
			user, err := models.FindUserByEmailAndAudience(ts.API.db, ts.instanceID, "test@example.com", ts.Config.JWT.Aud)
			ts.Require().NoError(err)
			require.NoError(ts.T(), user.EnableMFA(ts.API.db))

			token, err := generateAccessToken(user, time.Second*time.Duration(ts.Config.JWT.Exp), ts.Config.JWT.Secret)
			require.NoError(ts.T(), err)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/mfa/%s/factor", user.ID), &buffer)
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
			req.Header.Set("Content-Type", "application/json")
			ts.API.handler.ServeHTTP(w, req)
			require.Equal(ts.T(), http.StatusOK, w.Code)

			factors, err := models.FindFactorsByUser(ts.API.db, user)
			ts.Require().NoError(err)
			latestFactor := factors[len(factors)-1]
			require.Equal(ts.T(), models.FactorDisabledState, latestFactor.Status)
			if c.FriendlyName != "" {
				require.Equal(ts.T(), c.FriendlyName, latestFactor.FriendlyName)
			}
		})
	}

}

func (ts *MFATestSuite) TestChallengeFactor() {
	cases := []struct {
		desc         string
		id           string
		mfaEnabled   bool
		expectedCode int
	}{
		{
			"MFA Not Enabled",
			"",
			false,
			http.StatusForbidden,
		},
		{
			"Factor ID present",
			"testFactorID",
			true,
			http.StatusOK,
		},
		{
			"Factor ID missing",
			"",
			true,
			http.StatusUnprocessableEntity,
		},
	}
	for _, c := range cases {
		ts.Run(c.desc, func() {
			u, err := models.FindUserByEmailAndAudience(ts.API.db, ts.instanceID, "test@example.com", ts.Config.JWT.Aud)
			require.NoError(ts.T(), err)

			if c.mfaEnabled {
				require.NoError(ts.T(), u.EnableMFA(ts.API.db), "Error setting MFA to disabled")
			}

			token, err := generateAccessToken(u, time.Second*time.Duration(ts.Config.JWT.Exp), ts.Config.JWT.Secret)
			require.NoError(ts.T(), err, "Error generating access token")

			var buffer bytes.Buffer
			require.NoError(ts.T(), json.NewEncoder(&buffer).Encode(map[string]interface{}{
				"factor_id": c.id,
			}))

			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost/mfa/%s/challenge", u.ID), &buffer)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

			w := httptest.NewRecorder()
			ts.API.handler.ServeHTTP(w, req)
			require.Equal(ts.T(), c.expectedCode, w.Code)
		})
	}
}

func (ts *MFATestSuite) TestMFAVerifyFactor() {
	cases := []struct {
		desc             string
		validChallenge   bool
		validCode        bool
		expectedHTTPCode int
	}{
		{
			"Invalid: Valid code and expired challenge",
			false,
			true,
			http.StatusUnauthorized,
		},
		{
			"Invalid: Invalid code and valid challenge ",
			true,
			false,
			http.StatusUnauthorized,
		},
		{
			"Valid /verify request",
			true,
			true,
			http.StatusOK,
		},
	}
	for _, v := range cases {
		ts.Run(v.desc, func() {
			// Create a User with MFA enabled
			u, err := models.FindUserByEmailAndAudience(ts.API.db, ts.instanceID, "test@example.com", ts.Config.JWT.Aud)
			require.NoError(ts.T(), u.EnableMFA(ts.API.db))
			emailValue, err := u.Email.Value()
			require.NoError(ts.T(), err)
			testEmail := emailValue.(string)
			testDomain := strings.Split(testEmail, "@")[1]
			// set factor secret
			key, err := totp.Generate(totp.GenerateOpts{
				Issuer:      testDomain,
				AccountName: testEmail,
			})
			sharedSecret := key.Secret()
			factors, err := models.FindFactorsByUser(ts.API.db, u)
			f := factors[0]
			f.SecretKey = sharedSecret
			require.NoError(ts.T(), err)
			require.NoError(ts.T(), ts.API.db.Update(f), "Error updating new test factor")

			// Make a challenge
			c, err := models.NewChallenge(f)
			require.NoError(ts.T(), err, "Error creating test Challenge model")
			require.NoError(ts.T(), ts.API.db.Create(c), "Error saving new test challenge")
			if !v.validChallenge {
				// Set challenge creation so that it has expired in present time.
				newCreatedAt := time.Now().UTC().Add(-1 * time.Second * time.Duration(ts.Config.MFA.ChallengeExpiryDuration+1))
				// created_at is managed by buffalo(ORM) needs to be raw query toe be updated
				err := ts.API.db.RawQuery("UPDATE auth.mfa_challenges SET created_at = ? WHERE factor_id = ?", newCreatedAt, f.ID).Exec()
				require.NoError(ts.T(), err, "Error updating new test challenge")
			}

			// Verify the user
			user, err := models.FindUserByEmailAndAudience(ts.API.db, ts.instanceID, testEmail, ts.Config.JWT.Aud)
			ts.Require().NoError(err)
			code, err := totp.GenerateCode(sharedSecret, time.Now().UTC())
			if !v.validCode {
				// Use an inaccurate time, resulting in an invalid code(usually)
				code, err = totp.GenerateCode(sharedSecret, time.Now().UTC().Add(-1*time.Minute*time.Duration(1)))
			}
			require.NoError(ts.T(), err)
			var buffer bytes.Buffer
			require.NoError(ts.T(), json.NewEncoder(&buffer).Encode(map[string]interface{}{
				"challenge_id": c.ID,
				"code":         code,
			}))

			token, err := generateAccessToken(user, time.Second*time.Duration(ts.Config.JWT.Exp), ts.Config.JWT.Secret)
			require.NoError(ts.T(), err)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/mfa/%s/verify", user.ID), &buffer)
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
			ts.API.handler.ServeHTTP(w, req)
			require.Equal(ts.T(), v.expectedHTTPCode, w.Code)

			// Check response
			data := VerifyFactorResponse{}
			if v.expectedHTTPCode == http.StatusOK {
				require.NoError(ts.T(), json.NewDecoder(w.Body).Decode(&data))
				require.Equal(ts.T(), data.Success, "true")
			}
			if !v.validChallenge {
				_, err := models.FindChallengeByChallengeID(ts.API.db, c.ID)
				require.EqualError(ts.T(), err, models.ChallengeNotFoundError{}.Error())
			}
		})
	}
}