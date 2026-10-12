export function SignIn() {
  return (
    <div className="signin">
      <p className="brand">agentbox</p>
      <h1>Sign in</h1>
      <p>
        Run <code>agentbox ui</code> in a terminal to open a signed-in dashboard.
      </p>
      <p className="muted">
        Or paste the contents of <code>~/.agentbox/token</code>:
      </p>
      <form method="post" action="/ui/login" className="row">
        <input type="password" name="token" placeholder="token" autoComplete="off" spellCheck={false} />
        <button className="primary">Sign in</button>
      </form>
    </div>
  )
}
