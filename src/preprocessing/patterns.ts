// Secret detection patterns

export interface SecretPattern {
  name: string
  pattern: RegExp
  // Some patterns need context awareness (e.g., AWS secret keys near access keys)
  contextRequired?: boolean
}

export const SECRET_PATTERNS: SecretPattern[] = [
  // AWS
  {
    name: 'aws_access_key',
    pattern: /\b(AKIA[0-9A-Z]{16})\b/g,
  },
  {
    name: 'aws_secret_key',
    pattern:
      /(?:aws[_-]?secret[_-]?(?:access[_-]?)?key|secret[_-]?key)["\s:=]+["']?([A-Za-z0-9/+=]{40})["']?/gi,
  },

  // GitHub
  {
    name: 'github_token',
    pattern: /\b(gh[pousr]_[A-Za-z0-9_]{36,255})\b/g,
  },
  {
    name: 'github_oauth',
    pattern: /\b(gho_[A-Za-z0-9]{36,255})\b/g,
  },

  {
    name: 'github_fine_grained_token',
    pattern: /\b(github_pat_[A-Za-z0-9_]{22,255})\b/g,
  },

  // GitLab
  {
    name: 'gitlab_token',
    pattern: /\b(glpat-[A-Za-z0-9\-_]{20,})\b/g,
  },

  // Slack
  {
    name: 'slack_token',
    pattern: /\b(xox[baprs]-[0-9]{10,13}-[0-9]{10,13}-[a-zA-Z0-9]{24,})\b/g,
  },
  {
    name: 'slack_webhook',
    pattern:
      /(https:\/\/hooks\.slack\.com\/services\/T[A-Z0-9]+\/B[A-Z0-9]+\/[a-zA-Z0-9]+)/g,
  },

  // Discord
  {
    name: 'discord_token',
    pattern: /\b([MN][A-Za-z\d]{23,}\.[\w-]{6}\.[\w-]{27,})\b/g,
  },
  {
    name: 'discord_webhook',
    pattern:
      /(https:\/\/discord(?:app)?\.com\/api\/webhooks\/\d+\/[A-Za-z0-9_-]+)/g,
  },

  // JWTs
  {
    name: 'jwt',
    pattern: /\b(eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+)\b/g,
  },

  // Generic Bearer/Basic Auth
  {
    name: 'bearer_token',
    pattern: /\bBearer\s+([A-Za-z0-9_\-.]{20,})\b/gi,
  },
  {
    name: 'basic_auth',
    pattern: /\bBasic\s+([A-Za-z0-9+/=]{20,})\b/gi,
  },

  // Connection strings
  {
    name: 'connection_string',
    pattern:
      /\b((?:mongodb|postgres|postgresql|mysql|redis|amqp|amqps):\/\/[^:]+:[^@\s]+@[^\s"']+)\b/gi,
  },

  // Generic API keys (labeled)
  {
    name: 'api_key',
    pattern:
      /(?:api[_-]?key|apikey|api[_-]?secret)["\s:=]+["']?([A-Za-z0-9_\-]{20,})["']?/gi,
  },

  // Generic password fields
  {
    name: 'password',
    pattern:
      /(?:password|passwd|pwd|secret)["\s:=]+["']?([^\s"']{8,})["']?/gi,
  },

  // Private keys
  {
    name: 'private_key',
    pattern:
      /(-----BEGIN\s+(?:RSA\s+)?PRIVATE\s+KEY-----[\s\S]*?-----END\s+(?:RSA\s+)?PRIVATE\s+KEY-----)/g,
  },

  // Stripe
  {
    name: 'stripe_key',
    pattern: /\b(sk_(?:live|test)_[A-Za-z0-9]{24,})\b/g,
  },
  {
    name: 'stripe_restricted_key',
    pattern: /\b(rk_(?:live|test)_[A-Za-z0-9]{24,})\b/g,
  },

  // Sendgrid
  {
    name: 'sendgrid_key',
    pattern: /\b(SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43})\b/g,
  },

  // Twilio
  {
    name: 'twilio_key',
    pattern: /\b(SK[a-f0-9]{32})\b/g,
  },

  // OpenAI
  {
    name: 'openai_key',
    pattern: /\b(sk-[A-Za-z0-9]{32,})\b/g,
  },
  {
    name: 'openai_project_key',
    pattern: /\b(sk-proj-[A-Za-z0-9_-]{32,})\b/g,
  },

  // Anthropic
  {
    name: 'anthropic_key',
    pattern: /\b(sk-ant-[A-Za-z0-9_-]{32,})\b/g,
  },

  // Google
  {
    name: 'google_api_key',
    pattern: /\b(AIza[A-Za-z0-9_-]{35})\b/g,
  },

  // Heroku
  {
    name: 'heroku_key',
    pattern:
      /(?:heroku[_-]?api[_-]?key|HEROKU_API_KEY)["\s:=]+["']?([A-Fa-f0-9]{8}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{12})["']?/gi,
  },

  // NPM
  {
    name: 'npm_token',
    pattern: /\b(npm_[A-Za-z0-9]{36})\b/g,
  },

  // High-entropy strings that look like secrets (last resort, more specific patterns take precedence)
  // This catches things like randomly generated tokens
  {
    name: 'high_entropy_secret',
    pattern:
      /(?:token|secret|key|auth|credential)["\s:=]+["']?([A-Za-z0-9_\-]{32,64})["']?/gi,
  },
]
