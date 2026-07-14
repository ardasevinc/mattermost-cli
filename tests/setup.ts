import { afterEach } from 'vitest'
import { setActiveMattermostCredential } from '../src/preprocessing'

afterEach(() => setActiveMattermostCredential(undefined))
