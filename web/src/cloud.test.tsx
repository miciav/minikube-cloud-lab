import {render,screen} from '@testing-library/react';
import {it,expect,vi} from 'vitest';
import {App} from './App';
it('shows cloud overview and recoverable API error',async()=>{vi.stubGlobal('fetch',vi.fn().mockRejectedValue(Error('Cloud unavailable')));render(<App/>);expect(screen.getByText('Cloud Lab')).toBeTruthy();expect(await screen.findByRole('alert')).toBeTruthy();expect(screen.getByRole('button',{name:'Retry'})).toBeTruthy();expect(screen.queryByText('Install autoscaler')).toBeNull()});

