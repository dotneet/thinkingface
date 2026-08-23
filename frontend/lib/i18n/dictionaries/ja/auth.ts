// auth: login / sign-up.
export const auth = {
  welcome: "🤔 Thinking Face へようこそ",
  loginTab: "ログイン",
  signupTab: "新規登録",
  username: "ユーザー名",
  email: "メールアドレス",
  password: "パスワード",
  usernameLabel: "ユーザー名（あなたの名前空間）",
  usernameHint:
    "そのまま名前空間になります: プロフィールは /{username}、リポジトリは {username}/<repo> という名前になります。あとから変更はできません。英数字・ドット・ハイフン・アンダースコアで 1〜96 文字。",
  usernamePlaceholder: "alice",
  usernamePermanent: "ユーザー名は変更できません。表示名はいつでも変更できます。",
  submitLogin: "ログイン",
  submitSignup: "アカウントを作成",
  pleaseWait: "お待ちください…",
  preview: {
    profileLabel: "プロフィール",
    repositoriesLabel: "リポジトリ",
  },
  availability: {
    checking: "利用可否を確認中…",
    available: "{name} は利用できます",
    taken: "{name} はすでに使われています（大文字小文字は区別されません）",
  },
  errors: {
    invalidCredentials: "ユーザー名またはパスワードが正しくありません。",
    passwordTooShort: "パスワードは 8 文字以上で入力してください。",
    usernameRequired: "ユーザー名を入力してください。",
    usernameInvalid:
      "ユーザー名は 1〜96 文字の英数字・ドット・ハイフン・アンダースコアで、先頭は英数字にしてください。",
    usernameGitSuffix: "ユーザー名の末尾を .git にすることはできません。",
    usernameReserved: "そのユーザー名はこのサーバーで予約されています。別の名前にしてください。",
  },
};
