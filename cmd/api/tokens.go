package main

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/JeremiahH34/GreenlightApi/internal/data"
	"github.com/JeremiahH34/GreenlightApi/internal/validator"

	"github.com/pascaldekloe/jwt"
)

func (app *application) createAuthenticationToken(w http.ResponseWriter, r *http.Request) {
	// Parse the email and password from the request body
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	err := app.readJson(w, r, &input)
	if err != nil {
		app.badRequestResponse(w, r, err)
		return
	}

	v := validator.New()

	data.ValidateEmail(v, input.Email)
	data.ValidatePasswordPlaintext(v, input.Password)

	if !v.Valid() {
		app.failedValidationResponse(w, r, v.Errors)
		return
	}

	// Lookup the user record based on the email address. If no matching user was
	// found, then we call the app.invalidCredentialsResponse() helper to send a 401
	// Unauthorized response to the client (we will create this helper in a moment).
	user, err := app.models.Users.GetByEmail(input.Email)
	if err != nil {
		switch {
		case errors.Is(err, data.ErrRecordNotFound):
			app.invalidCredentialsResponse(w, r)
		default:
			app.serverErrorResponse(w, r, err)
		}
		return
	}

	// Check if the provided password matches the actual password for the user.
	match, err := user.Password.Matches(input.Password)
	if err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}

	// If the passwords don't match, then we call the app.invalidCredentialsResponse()
	// helper again and return.
	if !match {
		app.invalidCredentialsResponse(w, r)
		return
	}

	// Create a JWT claims struct containing the user ID as the subject, with an issued
	// time of now and validity window of the next 24 hours. We also set the issuer and
	// audience to a unique identifier for our application.
	var claims jwt.Claims
	claims.Subject = strconv.FormatInt(user.ID, 10)
	claims.Issued = jwt.NewNumericTime(time.Now())
	claims.NotBefore = jwt.NewNumericTime(time.Now())
	claims.Expires = jwt.NewNumericTime(time.Now().Add(24 * time.Hour))
	claims.Issuer = "github.com/JeremiahH34/GreenlightApi"
	claims.Audiences = []string{"github.com/JeremiahH34/GreenlightApi"}

	// // Otherwise, if the password is correct, we generate a new token with a 24-hour
	// // expiry time and the scope 'authentication'.
	// token, err := app.models.Tokens.New(user.ID, 24*time.Hour, data.ScopeAuthentication)
	// if err != nil {
	// 	app.serverErrorResponse(w, r, err)
	// }

	// Sign the JWT claims using the HMAC-SHA256 algorithm and the secret key from the
	// application config. This returns a []byte slice containing the JWT as a base64-
	// encoded string.
	jwtBytes, err := claims.HMACSign(jwt.HS256, []byte(app.config.jwt.secret))
	if err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}

	// // Encode the token to JSON and send it in the response along with a 201 Created
	// // status code.
	// err = app.writeJson(w, http.StatusCreated, envelope{"authentication_token": token}, nil)
	// if err != nil {
	// 	app.serverErrorResponse(w, r, err)
	// }

	err = app.writeJson(w, http.StatusCreated, envelope{"authentication_token": string(jwtBytes)}, nil)
	if err != nil {
		app.serverErrorResponse(w, r, err)
	}
}

func (app *application) createResetPasswordTokenHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}

	err := app.readJson(w, r, &input)
	if err != nil {
		app.badRequestResponse(w, r, err)
		return
	}

	v := validator.New()

	if data.ValidateEmail(v, input.Email); !v.Valid() {
		app.failedValidationResponse(w, r, v.Errors)
		return
	}

	user, err := app.models.Users.GetByEmail(input.Email)
	if err != nil {
		switch {
		case errors.Is(err, data.ErrRecordNotFound):
			v.AddError("email", "no matching email address")
			app.failedValidationResponse(w, r, v.Errors)
		default:
			app.serverErrorResponse(w, r, err)
		}

		return
	}

	if !user.Activated {
		v.AddError("email", "user account must be activated")
		app.failedValidationResponse(w, r, v.Errors)
		return
	}

	token, err := app.models.Tokens.New(user.ID, 45*time.Minute, data.ScopePasswordReset)
	if err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}

	app.background(func() {
		data := map[string]any{
			"passwordResetToken": token.Plaintext,
		}

		err = app.mailer.Send(user.Email, "token_password_reset.tmpl", data)
		if err != nil {
			app.logger.Error(err.Error())
		}
	})

	env := envelope{"message": "an email will be sent to you containing password reset instructions"}

	err = app.writeJson(w, http.StatusAccepted, env, nil)
	if err != nil {
		app.serverErrorResponse(w, r, err)
	}
}

func (app *application) createActivationTokenHandler(w http.ResponseWriter, r *http.Request) {
	// Parse and validate the user's email address.
	var input struct {
		Email string `json:"email"`
	}

	err := app.readJson(w, r, &input)
	if err != nil {
		app.badRequestResponse(w, r, err)
		return
	}

	v := validator.New()

	if data.ValidateEmail(v, input.Email); !v.Valid() {
		app.failedValidationResponse(w, r, v.Errors)
		return
	}

	// Try to retrieve the corresponding user record for the email address. If it can't
	// be found, return an error message to the client.
	user, err := app.models.Users.GetByEmail(input.Email)
	if err != nil {
		switch {
		case errors.Is(err, data.ErrRecordNotFound):
			v.AddError("email", "no matching email")
			app.failedValidationResponse(w, r, v.Errors)
		default:
			app.serverErrorResponse(w, r, err)
		}

		return
	}

	// Return an error if the user has already been activated.
	if user.Activated {
		v.AddError("email", "user has already been activated")
		app.failedValidationResponse(w, r, v.Errors)
		return
	}

	// Otherwise, create a new activation token.
	token, err := app.models.Tokens.New(user.ID, 3*24*time.Hour, data.ScopeActivation)
	if err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}

	// Email the user with their additional activation token.
	app.background(func() {
		data := map[string]any{
			"activationToken": token.Plaintext,
		}

		// Since email addresses MAY be case sensitive, notice that we are sending this
		// email using the address stored in our database for the user --- not to the
		// input.Email address provided by the client in this request.
		err = app.mailer.Send(user.Email, "token_activation.tmpl", data)
		if err != nil {
			app.logger.Error(err.Error())
		}
	})

	// Send a 202 Accepted response and confirmation message to the client.
	env := envelope{"message": "an email will be sent to you containing activiation instructions"}

	err = app.writeJson(w, http.StatusAccepted, env, nil)
	if err != nil {
		app.serverErrorResponse(w, r, err)
	}
}
