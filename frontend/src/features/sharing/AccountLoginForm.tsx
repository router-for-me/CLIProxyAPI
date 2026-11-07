import { useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { useAuthStore, useNotificationStore } from '@/stores';
import { sharingApi } from '@/services/api/sharing';
import { loginRedirectTarget } from '@/router/location';

export function AccountLoginForm({
  apiBase,
  needsSetup,
}: {
  apiBase: string;
  needsSetup: boolean;
}) {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const login = useAuthStore((state) => state.loginAccount);
  const notify = useNotificationStore((state) => state.showNotification);
  const [registering, setRegistering] = useState(needsSetup);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [invite, setInvite] = useState('');
  const [remember, setRemember] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setLoading(true);
    setError('');
    try {
      if (registering) {
        await sharingApi.register({ username, password, invite });
        notify(t('sharing.registered'), 'success');
      }
      await login(apiBase, username, password, remember);
      navigate(loginRedirectTarget(location.state), { replace: true });
    } catch (error) {
      setError(error instanceof Error ? error.message : String(error));
    } finally {
      setLoading(false);
    }
  };

  return (
    <form onSubmit={(event) => void submit(event)}>
      <Input
        autoFocus
        label={t('sharing.username')}
        name="username"
        autoComplete="username"
        pattern="[a-z][a-z0-9_]{2,31}"
        required
        value={username}
        onChange={(event) => setUsername(event.target.value)}
        hint={registering ? t('sharing.username_hint') : undefined}
      />
      <Input
        label={t('sharing.password')}
        type="password"
        name="password"
        required
        minLength={8}
        maxLength={72}
        autoComplete={registering ? 'new-password' : 'current-password'}
        value={password}
        onChange={(event) => setPassword(event.target.value)}
      />
      {registering && !needsSetup && (
        <Input
          label={t('sharing.invite')}
          required
          value={invite}
          onChange={(event) => setInvite(event.target.value)}
        />
      )}
      {registering && needsSetup && <p className="hint">{t('sharing.first_admin')}</p>}
      <div className="form-group">
        <SelectionCheckbox
          checked={remember}
          onChange={setRemember}
          ariaLabel={t('login.remember_password_label')}
          label={t('login.remember_password_label')}
        />
      </div>
      <Button type="submit" fullWidth loading={loading}>
        {t(registering ? 'sharing.register' : 'sharing.login')}
      </Button>
      <Button
        type="button"
        variant="ghost"
        fullWidth
        disabled={loading}
        onClick={() => {
          setRegistering(!registering);
          setError('');
        }}
      >
        {t(registering ? 'sharing.back_login' : 'sharing.invite_register')}
      </Button>
      {error && (
        <div className="error-box" role="alert">
          {error}
        </div>
      )}
    </form>
  );
}
