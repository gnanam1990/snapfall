import { redirect } from 'next/navigation';

/** There is no landing page: the dashboard opens straight onto the live overview. */
export default function Root() {
  redirect('/overview');
}
